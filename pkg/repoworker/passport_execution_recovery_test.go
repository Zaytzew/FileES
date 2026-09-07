package repoworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	control "filees/pkg/control/v1"
	"filees/pkg/passport"
	"github.com/google/uuid"
)

type executionLostClient struct{ *recoverySVNClient }

func (c *executionLostClient) ConfirmLock(context.Context, string, string, string) (*client.LockInfo, error) {
	return nil, errors.New("lost WC/remote observation")
}

func TestExecutionManagerRealSVNRestartLostSettlementAfterRevoke(t *testing.T) {
	f := newExecutionFixture(t)
	wc := filepath.Dir(filepath.Dir(f.doc))
	preparations := &PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	arms, settles := 0, 0
	x := recoveryExchange(func(ctx context.Context, ticket control.Ticket) (control.Result, error) {
		// Exercise the strict wire parser and real worker dispatch too.
		raw, _ := json.Marshal(ticket)
		var out bytes.Buffer
		d := Dispatcher{Worker: &Worker{PassportExecutions: &f.svc, PassportPreparations: preparations}, Resolver: preparationResolver{f.session}, Admission: preparationAdmission{}}
		if err := d.Serve(ctx, f.session.ClientID, bytes.NewReader(raw), &out); err != nil {
			return control.Result{}, err
		}
		if ticket.Type == control.TicketArmPassportAcquisition {
			arms++
		}
		if ticket.Type == control.TicketSettlePassportAcquisition {
			settles++
			if settles == 1 {
				return control.Result{}, errors.New("lost durable closed response")
			}
		}
		return control.ParseResult(out.Bytes())
	})
	cli := &executionLostClient{&recoverySVNClient{replacementSVNClient: &replacementSVNClient{Client: client.New(client.Options{Timeout: 10 * time.Second}), username: f.session.ClientID}, loseReply: true}}
	backend := passport.ControlSVNBackend{FenceAcquisitions: true, SVNBackend: passport.SVNBackend{Client: cli, WC: wc}, RepoID: f.req.RepoID, ClientID: f.session.ClientID, Transport: x}
	store, instance := filepath.Join(t.TempDir(), "passports.json"), uuid.NewString()
	now := time.Now().UTC()
	open := func() *passport.Manager {
		m, err := passport.Open(store, instance, backend, passport.Config{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := open()
	if _, _, err := m.Acquire(t.Context(), []string{f.doc}, ""); err == nil {
		t.Fatal("lost lock reply accepted")
	}
	if len(m.Snapshot()) != 1 || m.Snapshot()[0].Pending.Stage != "locking" {
		t.Fatal("missing locking intent")
	}
	// Transport activation remains, but a new view contains no repo grant.
	f.session.Repositories = nil
	m = open()
	if _, _, err := m.Acquire(t.Context(), []string{f.doc}, ""); err == nil {
		t.Fatal("lost settlement accepted")
	}
	if len(m.Snapshot()) != 1 || m.Snapshot()[0].Pending.Stage != "settling" {
		t.Fatal("missing durable closing direction")
	}
	now = now.Add(-time.Hour)
	m = open()
	if _, _, err := m.Acquire(t.Context(), []string{f.doc}, ""); err == nil {
		t.Fatal("aborted acquisition reported success")
	}
	if len(m.Snapshot()) != 0 || len(open().Snapshot()) != 0 || arms != 1 || settles != 2 || cli.locks != 1 || cli.forceCalls != 0 {
		t.Fatalf("replayed/not durable: arms=%d settles=%d locks=%d", arms, settles, cli.locks)
	}
	if lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path); err != nil || lock != nil {
		t.Fatalf("unsettled lock: %+v %v", lock, err)
	}
	if data, _ := os.ReadFile(f.doc); string(data) != "local work" {
		t.Fatal("changed data")
	}
}

func TestExecutionGenerationSwapPreservesNewLock(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	old, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	if err := f.authority.Locks.unlockIfCurrent(t.Context(), f.req.RepoID, f.req.Path, old.Owner, old.Token); err != nil {
		t.Fatal(err)
	}
	replacementCommand(t, "svnadmin", "setuuid", f.repo, uuid.NewString())
	replacementCommand(t, "svn", "lock", "--username", uuid.NewString(), "-m", "new generation", "file://"+f.repo+"/"+f.req.Path)
	before, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
	after, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if before == nil || after == nil || before.Token != after.Token {
		t.Fatal("new generation token changed")
	}
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultError)
}

func TestExecutionInterruptedUnlockDoesNotSubstituteNewToken(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	f.svc.Authority.Locks.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := runLockAuthorityCommand(ctx, name, args...)
		if args[0] == "unlock" && err == nil {
			return nil, errors.New("lost svnadmin response")
		}
		return out, err
	}
	ticket := f.arm
	ticket.Type = control.TicketSettlePassportAcquisition
	if _, err := f.svc.Handle(t.Context(), f.session, ticket); err == nil {
		t.Fatal("lost unlock acknowledged")
	}
	r := f.record(t)
	if r.State != "closing" || r.ObservedToken == "" {
		t.Fatal("token fence missing")
	}
	replacementCommand(t, "svn", "lock", "--username", f.session.ClientID, "-m", "new ordinary lock", f.doc)
	before, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	f.svc.Authority.Locks.Run = nil
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
	after, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if before == nil || after == nil || before.Token != after.Token {
		t.Fatal("substituted newer token")
	}
}

func TestExecutionReapIsolatesCorruptRecordAndRetainsTombstone(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	file := filepath.Join(executionDirectory(f.repo), uuid.NewString()+".json")
	if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	f.svc.Authority.Now = func() time.Time { return f.metadata.ExpiresAt }
	if err := f.svc.ReapRepository(t.Context(), f.req.RepoID); err == nil {
		t.Fatal("corruption hidden")
	}
	if f.record(t).State != "closed" {
		t.Fatal("corruption blocked independent cleanup")
	}
	if _, err := os.Stat(filepath.Join(executionDirectory(f.repo), f.metadata.AcquisitionID+".lock")); err != nil {
		t.Fatal("lock fence garbage collected")
	}
}
