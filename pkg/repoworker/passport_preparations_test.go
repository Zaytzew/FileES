package repoworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
	"github.com/google/uuid"
)

func preparationTicket(t *testing.T, f *replacementFixture) control.Ticket {
	t.Helper()
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketPreparePassportReplacement, f.session.ClientID, control.PreparePassportReplacementPayload{
		RepoID: f.req.RepoID, Path: f.req.Path, ObservedLockID: f.req.ObservedToken, PassportID: f.req.PassportID, InstanceUID: f.req.InstanceUID, Mode: f.req.Mode,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func requirePreparationCode(t *testing.T, r control.Result, err error, code errcat.Code) {
	t.Helper()
	if err != nil || r.Error == nil || r.Error.Code != string(code) {
		t.Fatalf("result=%+v error=%v want=%s", r, err, code)
	}
}

func TestPassportPreparationConcurrentReplayAndBinding(t *testing.T) {
	f := newReplacementFixture(t)
	svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	ticket := preparationTicket(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := svc.Handle(t.Context(), f.session, ticket)
			if err != nil || r.Status != control.ResultOK {
				t.Errorf("prepare=%+v err=%v", r, err)
			}
		}()
	}
	wg.Wait()
	if f.unlocks != 1 {
		t.Fatalf("unlock count=%d", f.unlocks)
	}
	changed := ticket
	changed.RequestID = uuid.NewString()
	r, err := svc.Handle(t.Context(), f.session, changed)
	requirePreparationCode(t, r, err, errcat.CodePassportRequestConflict)
	changed = ticket
	var p control.PreparePassportReplacementPayload
	_ = json.Unmarshal(changed.Payload, &p)
	p.Path = "docs/another.txt"
	changed.Payload, _ = json.Marshal(p)
	r, err = svc.Handle(t.Context(), f.session, changed)
	requirePreparationCode(t, r, err, errcat.CodePassportRequestConflict)
	other := f.session
	other.ClientID = uuid.NewString()
	f.client(t, other.ClientID, f.owner, "active")
	changed = ticket
	changed.ClientID = other.ClientID
	r, err = svc.Handle(t.Context(), other, changed)
	requirePreparationCode(t, r, err, errcat.CodePassportRequestConflict)
	f.client(t, f.session.ClientID, f.owner, "revoked")
	r, err = svc.Handle(t.Context(), f.session, ticket)
	requirePreparationCode(t, r, err, errcat.CodePassportDenied)
	if f.unlocks != 1 {
		t.Fatal("replay or wrong actor mutated reservation")
	}
}

func TestPassportPreparationCrashFenceAndUnavailableStorage(t *testing.T) {
	for _, scenario := range []string{"intent-only", "failure-after-unlock", "storage", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReplacementFixture(t)
			svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
			ticket := preparationTicket(t, f)
			switch scenario {
			case "intent-only":
				if err := atomicJSON(filepath.Join(svc.Root, ticket.OperationID+".json"), passportPreparationRecord{Schema: "filees.passport-preparation/v1", Digest: preparationDigest(f.session, ticket), State: "started"}); err != nil {
					t.Fatal(err)
				}
				r, err := svc.Handle(t.Context(), f.session, ticket)
				requirePreparationCode(t, r, err, errcat.CodePassportAborted)
			case "failure-after-unlock":
				run := svc.Authority.Locks.Run
				svc.Authority.Locks.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					raw, err := run(ctx, name, args...)
					if args[0] == "unlock" && err == nil {
						return nil, errors.New("lost unlock response")
					}
					return raw, err
				}
				if _, err := svc.Handle(t.Context(), f.session, ticket); err == nil {
					t.Fatal("lost response acknowledged")
				}
				r, err := (PassportPreparations{Root: svc.Root, Authority: f.authority}).Handle(t.Context(), f.session, ticket)
				requirePreparationCode(t, r, err, errcat.CodePassportAborted)
				if f.unlocks != 1 {
					t.Fatalf("replayed unlock=%d", f.unlocks)
				}
				return
			case "storage":
				blocked := filepath.Join(svc.Root, "not-a-directory")
				if err := os.WriteFile(blocked, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				svc.Root = blocked
				if _, err := svc.Handle(t.Context(), f.session, ticket); err == nil {
					t.Fatal("unavailable journal accepted")
				}
			case "canceled":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if _, err := svc.Handle(ctx, f.session, ticket); err == nil {
					t.Fatal("canceled mutation accepted")
				}
			}
			if f.unlocks != 0 {
				t.Fatal("mutation without durable admission")
			}
		})
	}
}

func TestAbortedPreparationIsDurableBoundAndNeverReplayed(t *testing.T) {
	f := newReplacementFixture(t)
	svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	ticket := preparationTicket(t, f)
	path := filepath.Join(svc.Root, ticket.OperationID+".json")
	if err := atomicJSON(path, passportPreparationRecord{Schema: "filees.passport-preparation/v1", Digest: preparationDigest(f.session, ticket), State: "started"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := svc.Handle(t.Context(), f.session, ticket)
			if err != nil || r.Error == nil || r.Error.Code != string(errcat.CodePassportAborted) {
				t.Errorf("terminal receipt: %+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	var record passportPreparationRecord
	if err := decodeJSONFile(path, &record); err != nil || record.State != "finished" || record.Result == nil {
		t.Fatalf("not durable: %+v %v", record, err)
	}
	before, _ := os.ReadFile(path)
	other := ticket
	other.RequestID = uuid.NewString()
	r, err := svc.Handle(t.Context(), f.session, other)
	requirePreparationCode(t, r, err, errcat.CodePassportRequestConflict)
	r, err = (PassportPreparations{Root: svc.Root, Authority: f.authority}).Handle(t.Context(), f.session, ticket)
	requirePreparationCode(t, r, err, errcat.CodePassportAborted)
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || f.unlocks != 0 {
		t.Fatal("terminal replay rewrote receipt or mutated lock")
	}
}

func TestAbortedPreparationCannotAcknowledgeFailedSave(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission failure fixture requires non-root")
	}
	f := newReplacementFixture(t)
	svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	ticket := preparationTicket(t, f)
	path := filepath.Join(svc.Root, ticket.OperationID+".json")
	if err := atomicJSON(path, passportPreparationRecord{Schema: "filees.passport-preparation/v1", Digest: preparationDigest(f.session, ticket), State: "started"}); err != nil {
		t.Fatal(err)
	}
	if err := WithFileLock(filepath.Join(svc.Root, ticket.OperationID+".lock"), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(svc.Root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(svc.Root, 0700)
	if _, err := svc.Handle(t.Context(), f.session, ticket); err == nil {
		t.Fatal("failed terminal save acknowledged")
	}
	var record passportPreparationRecord
	if err := decodeJSONFile(path, &record); err != nil || record.State != "started" {
		t.Fatalf("pending record lost: %+v %v", record, err)
	}
	if err := os.Chmod(svc.Root, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := svc.Handle(t.Context(), f.session, ticket)
	requirePreparationCode(t, r, err, errcat.CodePassportAborted)
	if f.unlocks != 0 {
		t.Fatal("retry mutated lock")
	}
}

type preparationResolver struct{ session Session }

func (r preparationResolver) Resolve(string) (Session, error) { return r.session, nil }

type preparationAdmission struct{ denied bool }

func (a preparationAdmission) Admit(Session, control.Ticket) error {
	if a.denied {
		return errors.New("realm removal fenced")
	}
	return nil
}

type lostPreparationWriter struct{}

func (lostPreparationWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPassportPreparationDispatcherLostReplyRealSVN(t *testing.T) {
	f, _, doc := realReplacementFixture(t)
	svc := &PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	ticket := preparationTicket(t, f)
	raw, _ := json.Marshal(ticket)
	d := Dispatcher{Worker: &Worker{PassportPreparations: svc}, Resolver: preparationResolver{f.session}}
	d.Admission = preparationAdmission{denied: true}
	if err := d.Serve(t.Context(), f.session.ClientID, bytes.NewReader(raw), io.Discard); err == nil {
		t.Fatal("realm fence ignored")
	}
	lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || lock == nil || lock.Token != f.req.ObservedToken {
		t.Fatalf("fenced lock=%+v %v", lock, err)
	}
	d.Admission = preparationAdmission{}
	if err := d.Serve(t.Context(), f.session.ClientID, bytes.NewReader(raw), lostPreparationWriter{}); err == nil {
		t.Fatal("simulated lost reply succeeded")
	}
	// Another client wins while the first client reconnects. Replay may return
	// the OLD receipt, but cannot release the newer reservation.
	newcomer := uuid.NewString()
	replacementCommand(t, "svn", "lock", "--username", newcomer, "-m", "new holder", doc)
	d.Worker = &Worker{PassportPreparations: &PassportPreparations{Root: svc.Root, Authority: f.authority}}
	var out bytes.Buffer
	if err := d.Serve(t.Context(), f.session.ClientID, bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	receipt, err := control.ParseResult(out.Bytes())
	if err != nil || receipt.Status != control.ResultOK {
		t.Fatalf("%s %v", out.String(), err)
	}
	lock, err = f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || lock == nil || lock.Owner != newcomer || lock.Token == f.req.ObservedToken {
		t.Fatalf("new lock lost: %+v %v", lock, err)
	}
	bytesOnDisk, err := os.ReadFile(doc)
	if err != nil || string(bytesOnDisk) != "local work" {
		t.Fatalf("data changed %q %v", bytesOnDisk, err)
	}
}

func TestPassportPreparationKnownDenials(t *testing.T) {
	for _, mode := range []string{"stale", "path-owner", "denied"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplacementFixture(t)
			want := errcat.CodePassportDenied
			switch mode {
			case "stale":
				f.metadata.PreviousToken = ""
				f.req.ObservedToken = "old-token"
				want = errcat.CodePassportStale
				run := f.authority.Locks.Run
				f.authority.Locks.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					raw, err := run(ctx, name, args...)
					return []byte(strings.ReplaceAll(string(raw), "old-token", "new-token")), err
				}
			case "path-owner":
				f.req.Mode = "migrate"
				f.authority.PathOwners = nil
				want = errcat.CodePathOwnerUnavailable
			case "denied":
				f.metadata.PassportID = uuid.NewString()
			}
			svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
			ticket := preparationTicket(t, f)
			r, err := svc.Handle(t.Context(), f.session, ticket)
			requirePreparationCode(t, r, err, want)
			if f.unlocks != 0 {
				t.Fatal("denial unlocked")
			}
		})
	}
}
