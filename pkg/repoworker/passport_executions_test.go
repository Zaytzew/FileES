package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
	"filees/pkg/passport"
	"github.com/google/uuid"
)

type executionFixture struct {
	*replacementFixture
	svc       PassportExecutions
	repo, doc string
	arm       control.Ticket
}

func newExecutionFixture(t *testing.T) *executionFixture {
	t.Helper()
	f, _, doc := realReplacementFixture(t)
	repo := filepath.Join(f.authority.Locks.RepositoriesRoot, f.req.RepoID)
	if err := f.authority.Prepare(t.Context(), f.session, f.req); err != nil {
		t.Fatal(err)
	}
	if err := InstallLockGuards(repo, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	f.metadata.AcquisitionID = uuid.NewString()
	arm, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketArmPassportAcquisition, f.session.ClientID,
		control.PassportExecutionPayload{RepoID: f.req.RepoID, Path: f.req.Path, Comment: passport.FormatComment(f.metadata)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &executionFixture{replacementFixture: f, svc: PassportExecutions{Authority: f.authority, GuardExecutable: os.Args[0]}, repo: repo, doc: doc, arm: arm}
}

func (f *executionFixture) handle(t *testing.T, kind control.TicketType, want control.ResultStatus) control.Result {
	t.Helper()
	ticket := f.arm
	ticket.Type = kind
	r, err := f.svc.Handle(t.Context(), f.session, ticket)
	if err != nil || r.Status != want {
		t.Fatalf("%s: %+v %v", kind, r, err)
	}
	return r
}

func (f *executionFixture) record(t *testing.T) passportExecution {
	t.Helper()
	r, err := readExecution(filepath.Join(executionDirectory(f.repo), f.metadata.AcquisitionID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *executionFixture) lock(t *testing.T) {
	t.Helper()
	replacementCommand(t, "svn", "lock", "--username", f.session.ClientID, "-m", passport.FormatComment(f.metadata), f.doc)
}

func TestPassportExecutionRealSVNOneShotAndLostReply(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	r := f.record(t)
	if r.State != "started" || r.ExecutorPID <= 1 || r.ExecutorPID == os.Getpid() {
		t.Fatalf("kernel executor not recorded: %+v", r)
	}
	// A lost SVN reply is settled without asking the WC to replay acquisition.
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
	if r := f.record(t); r.State != "closed" || r.ObservedToken == "" {
		t.Fatalf("not durable: %+v", r)
	}
	lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || lock != nil {
		t.Fatalf("not released: %+v %v", lock, err)
	}
	// Closed survives another worker and a server clock rollback.
	f.svc.Authority.Now = func() time.Time { return f.metadata.IssuedAt }
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultError)
	if out, err := exec.Command("svn", "lock", "--username", f.session.ClientID, "-m", passport.FormatComment(f.metadata), f.doc).CombinedOutput(); err == nil {
		t.Fatalf("late lock admitted: %s", out)
	}
	replacementCommand(t, "svn", "lock", "--username", uuid.NewString(), "-m", "ordinary competitor", f.doc)
	before, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
	after, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if before == nil || after == nil || before.Token != after.Token {
		t.Fatal("replay damaged competitor")
	}
	if data, _ := os.ReadFile(f.doc); string(data) != "local work" {
		t.Fatal("changed bytes")
	}
}

func TestPassportExecutionManualCommentUsesAuthenticatedRealm(t *testing.T) {
	f := newExecutionFixture(t)
	f.metadata.RealmID = ""
	f.arm.Payload, _ = json.Marshal(control.PassportExecutionPayload{RepoID: f.req.RepoID, Path: f.req.Path, Comment: passport.FormatComment(f.metadata)})
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	if f.record(t).RealmID != f.session.RealmID {
		t.Fatal("manual comment replaced authenticated realm")
	}
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
}

func TestPassportExecutionLiveExecutorAndDeath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process proof")
	}
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	// This process is the admitted executor stand-in; no process is signalled.
	args := []string{f.repo, "/" + f.req.Path, f.session.ClientID, passport.FormatComment(f.metadata), "0"}
	if err := AdmitPassportExecution(args, os.Getpid(), time.Now()); err != nil {
		t.Fatal(err)
	}
	r := f.handle(t, control.TicketSettlePassportAcquisition, control.ResultError)
	if r.Error.Code != string(errcat.CodePassportUncertain) || f.record(t).State != "closing" {
		t.Fatalf("live executor forgotten: %+v", r)
	}
	if err := AdmitPassportExecution(args, os.Getpid(), time.Now()); err == nil {
		t.Fatal("closing re-admitted")
	}
	// Use a reaped real child, not a guessed nonexistent PID.
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(executionDirectory(f.repo), f.metadata.AcquisitionID+".json")
	record := f.record(t)
	record.ExecutorPID = child.Process.Pid
	if err := atomicJSON(file, record); err != nil {
		t.Fatal(err)
	}
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
}

func TestPassportExecutionConcurrentAdmissionAndClosing(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	args := []string{f.repo, "/" + f.req.Path, f.session.ClientID, passport.FormatComment(f.metadata), "0"}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if AdmitPassportExecution(args, os.Getpid(), time.Now()) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("admissions=%d", successes.Load())
	}
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultError)
	if err := AdmitPassportExecution(args, os.Getpid(), time.Now()); err == nil {
		t.Fatal("fence reopened")
	}
}

func TestPassportExecutionExpiryServerClockAndLegacy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "journal", true: "legacy"}[legacy], func(t *testing.T) {
			f := newExecutionFixture(t)
			if legacy {
				f.metadata.AcquisitionID = ""
			} else {
				f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
			}
			f.lock(t)
			if err := f.svc.ExpirePath(t.Context(), f.req.RepoID, f.req.Path); err != nil {
				t.Fatal(err)
			}
			if lock, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path); lock == nil {
				t.Fatal("early expiry")
			}
			f.svc.Authority.Now = func() time.Time { return f.metadata.ExpiresAt }
			if err := f.svc.ExpirePath(t.Context(), f.req.RepoID, f.req.Path); err != nil {
				t.Fatal(err)
			}
			if lock, _ := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path); lock != nil {
				t.Fatal("expired lock remains")
			}
		})
	}
}

func TestPassportExecutionSettleBeforeArmAndRevoke(t *testing.T) {
	f := newExecutionFixture(t)
	f.session.RealmID = f.guest
	f.metadata.RealmID = f.guest
	f.client(t, f.session.ClientID, f.guest, "active")
	f.arm.Payload, _ = json.Marshal(control.PassportExecutionPayload{RepoID: f.req.RepoID, Path: f.req.Path, Comment: passport.FormatComment(f.metadata)})
	// No grant/view is required to fence this identity's own never-sent attempt.
	f.session.Repositories = nil
	f.handle(t, control.TicketSettlePassportAcquisition, control.ResultOK)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultError)
	f.client(t, f.session.ClientID, f.guest, "revoked")
	r := f.handle(t, control.TicketSettlePassportAcquisition, control.ResultError)
	if r.Error.Code != string(errcat.CodePassportDenied) {
		t.Fatal(r)
	}
}

func TestPassportExecutionExpiredArmCannotReturnAfterRollback(t *testing.T) {
	f := newExecutionFixture(t)
	f.svc.Authority.Now = func() time.Time { return f.metadata.ExpiresAt }
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultError)
	f.svc.Authority.Now = func() time.Time { return f.metadata.IssuedAt }
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultError)
	if f.record(t).State != "closed" {
		t.Fatal("expired arm reopened")
	}
}

func TestPassportExecutionFailedSaveAndCorruption(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix non-root permissions required")
	}
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	dir := executionDirectory(f.repo)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	args := []string{f.repo, "/" + f.req.Path, f.session.ClientID, passport.FormatComment(f.metadata), "0"}
	if err := AdmitPassportExecution(args, os.Getpid(), time.Now()); err == nil {
		t.Fatal("failed started write admitted")
	}
	ticket := f.arm
	ticket.Type = control.TicketSettlePassportAcquisition
	if _, err := f.svc.Handle(t.Context(), f.session, ticket); err == nil {
		t.Fatal("failed fence acknowledged")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, f.metadata.AcquisitionID+".json")
	raw, _ := os.ReadFile(file)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "executor_pid")
	if err := atomicJSON(file, fields); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Handle(t.Context(), f.session, ticket); err == nil {
		t.Fatal("corrupt record acknowledged")
	}
}

func TestPassportExecutionGenerationAndReaper(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	f.svc.Authority.Now = func() time.Time { return f.metadata.ExpiresAt }
	if err := f.svc.Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.record(t).State != "closed" {
		t.Fatal("reap did not close")
	}
	// Cancellation is bounded by context even during a tombstone scan.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.svc.Reap(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("context: %v", err)
	}
}
