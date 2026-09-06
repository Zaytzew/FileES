package repoworker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/errcat"
	"github.com/google/uuid"
)

func TestCancellationFencesLatePrepareWithoutSVN(t *testing.T) {
	for _, state := range []string{"missing", "started", "prepared", "denied"} {
		t.Run(state, func(t *testing.T) {
			f, _, doc := realReplacementFixture(t)
			svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
			original := preparationTicket(t, f)
			path := filepath.Join(svc.Root, original.OperationID+".json")
			switch state {
			case "started":
				if err := atomicJSON(path, passportPreparationRecord{Schema: "filees.passport-preparation/v1", Digest: preparationDigest(f.session, original), State: "started"}); err != nil {
					t.Fatal(err)
				}
			case "prepared":
				r, err := svc.Handle(t.Context(), f.session, original)
				if err != nil || r.Status != control.ResultOK {
					t.Fatalf("prepare: %+v %v", r, err)
				}
				replacementCommand(t, "svn", "lock", "--username", f.session.ClientID, "-m", "new holder", doc)
			case "denied":
				r, _ := preparationError(original, errcat.KeyPassportDenied, time.Now())
				if err := atomicJSON(path, passportPreparationRecord{Schema: "filees.passport-preparation/v1", Digest: preparationDigest(f.session, original), State: "finished", Result: &r}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
			if err != nil || before == nil {
				t.Fatalf("fixture lock: %+v %v", before, err)
			}
			svc.Authority.Locks.Run = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("cancellation invoked SVN")
				return nil, nil
			}
			x := recoveryExchange(func(ctx context.Context, ticket control.Ticket) (control.Result, error) {
				return (&Worker{PassportPreparations: &svc}).Handle(ctx, f.session, ticket)
			})
			for range 3 {
				if err := controlclient.CancelPassportPreparation(t.Context(), x, original); err != nil {
					t.Fatal(err)
				}
			}
			var record passportPreparationRecord
			if err := decodeJSONFile(path, &record); err != nil || record.State != "canceled" {
				t.Fatalf("not durable: %+v %v", record, err)
			}
			if (record.PriorResult != nil) != (state == "prepared" || state == "denied") {
				t.Fatal("previous receipt lost")
			}
			if state == "prepared" && record.PriorResult.Status != control.ResultOK {
				t.Fatal("success history changed")
			}
			// New worker, delayed original request: no mutation and never prepared.
			r, err := (PassportPreparations{Root: svc.Root, Authority: svc.Authority}).Handle(t.Context(), f.session, original)
			requirePreparationCode(t, r, err, errcat.CodePassportAborted)
			after, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
			if err != nil || after == nil || after.Token != before.Token {
				t.Fatalf("cancellation changed lock: %+v %v", after, err)
			}
			data, err := os.ReadFile(doc)
			if err != nil || string(data) != "local work" {
				t.Fatalf("bytes changed: %q %v", data, err)
			}
		})
	}
}

func TestCancellationSerializesWithPrepareAndBindsOriginal(t *testing.T) {
	f := newReplacementFixture(t)
	svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	original := preparationTicket(t, f)
	cancel, err := control.NewPassportCancellation(original)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r, err := svc.Handle(t.Context(), f.session, original)
			if err != nil || (r.Status != control.ResultOK && (r.Error == nil || r.Error.Code != string(errcat.CodePassportAborted))) {
				t.Errorf("prepare: %+v %v", r, err)
			}
		}()
		go func() {
			defer wg.Done()
			r, err := svc.Cancel(t.Context(), f.session, cancel)
			if err != nil || r.Status != control.ResultOK {
				t.Errorf("cancel: %+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	if f.unlocks > 1 {
		t.Fatal("mutation replayed")
	}
	r, err := svc.Handle(t.Context(), f.session, original)
	requirePreparationCode(t, r, err, errcat.CodePassportAborted)
	changed := original
	changed.RequestID = uuid.NewString()
	other, _ := control.NewPassportCancellation(changed)
	r, err = svc.Cancel(t.Context(), f.session, other)
	requirePreparationCode(t, r, err, errcat.CodePassportRequestConflict)
	// Neither a malformed record nor a revoked actor can obtain a receipt.
	f.client(t, f.session.ClientID, f.owner, "revoked")
	r, err = svc.Cancel(t.Context(), f.session, cancel)
	requirePreparationCode(t, r, err, errcat.CodePassportDenied)
	f.client(t, f.session.ClientID, f.owner, "active")
	path := filepath.Join(svc.Root, original.OperationID+".json")
	var record passportPreparationRecord
	if err := decodeJSONFile(path, &record); err != nil {
		t.Fatal(err)
	}
	record.Result = nil
	if err := atomicJSON(path, record); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(t.Context(), f.session, cancel); err == nil {
		t.Fatal("corrupt tombstone accepted")
	}
	if err := os.WriteFile(path, []byte(`{"state":"started"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(t.Context(), f.session, cancel); err == nil {
		t.Fatal("missing schema/digest inherited defaults")
	}
}

func TestCancellationFailedSaveDoesNotAcknowledge(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permission enforcement")
	}
	f := newReplacementFixture(t)
	svc := PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	original := preparationTicket(t, f)
	cancel, _ := control.NewPassportCancellation(original)
	if err := WithFileLock(filepath.Join(svc.Root, original.OperationID+".lock"), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(svc.Root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(svc.Root, 0700)
	if _, err := svc.Cancel(t.Context(), f.session, cancel); err == nil {
		t.Fatal("failed tombstone save acknowledged")
	}
	if _, err := os.Stat(filepath.Join(svc.Root, original.OperationID+".json")); !os.IsNotExist(err) {
		t.Fatal("unexpected tombstone")
	}
	if err := os.Chmod(svc.Root, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := svc.Cancel(t.Context(), f.session, cancel)
	if err != nil || r.Status != control.ResultOK || f.unlocks != 0 {
		t.Fatalf("cancel retry: %+v %v", r, err)
	}
}
