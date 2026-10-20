package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func repairAdmissionFixture(t *testing.T) (*daemonProvisioner, repositoryLifecycleService, localrepo.Record, *attachmentSVNStub) {
	t.Helper()
	root := t.TempDir()
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(root, "WIZUALIA-DUPLIKAT")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{filepath.Join(".svn", "wc.db"): "incomplete metadata", "drawing.dwg": "local edits"} {
		if err := os.WriteFile(filepath.Join(wc, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := store.BeginAttach("spot", "repo", wc, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.ApproveAttach(r.OperationID, r.ServerID, r.RepoID, "svn+ssh://example/repo", "rw")
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.MarkError(r.OperationID, errors.New("native SVN: invalid last_changed_rev receipt"))
	if err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(store, nil, []clientprofile.Profile{{ServerID: "spot"}})
	p.attachments = make(chan provisionedAttachment, 1)
	stub := &attachmentSVNStub{url: r.RepoURL, infoErr: errors.New(r.LastError)}
	p.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	service := repositoryLifecycleService{store: store, localIntentMu: &p.localIntentMu,
		anchorAdmissionWaiters: &p.creationWaiters, onRepair: p.RepairLifecycle}
	return p, service, r, stub
}

func waitRepairAdmission(t *testing.T, p *daemonProvisioner, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for p.creationWaiters.Load() == 0 {
		select {
		case err := <-done:
			t.Fatalf("repair refused instead of waiting: %v", err)
		case <-deadline.C:
			t.Fatal("repair priority absent")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRepairAdmissionIPCWaitsForBusyObserverAndPreservesIncompleteCopy(t *testing.T) {
	p, service, r, stub := repairAdmissionFixture(t)
	p.localIntentMu.Lock()
	locked := true
	defer func() {
		if locked {
			p.localIntentMu.Unlock()
		}
	}()
	socket := shortDaemonSocket(t)
	server := ipcserver.New(socket)
	server.SetRepositoryLifecycleService(service)
	state := server.RegisterProjectedRepo(r.RepoID, "WIZUALIA-SORTOWANE", r.RepoURL, r.ServerID, "rw", contract.StateInteractionRequired, false)
	state.SetLifecycleRepairMetadata(r.OperationID, r.LastError, true, true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "gui")
	done := make(chan error, 1)
	go func() {
		got, err := cli.RepoLifecycleRepair(ctx, contract.RepoLifecycleRepairPayload{OperationID: r.OperationID, ServerID: r.ServerID, RepoID: r.RepoID, Strategy: "abandon"})
		if err == nil && (got.OperationID != r.OperationID || got.State != string(localrepo.StateAbandoned)) {
			err = errors.New("wrong result")
		}
		done <- err
	}()
	waitRepairAdmission(t, p, done)
	stored, _ := p.local.Get(r.OperationID)
	if stored.State != r.State || stub.infoCalls != 0 {
		t.Fatal("executed before admission")
	}
	if summary := state.Summary(); summary.CanRetryLifecycle || summary.CanAbandonLifecycle {
		t.Fatal("duplicate action offered during wait")
	}
	if _, err := cli.RepoLifecycleRepair(ctx, contract.RepoLifecycleRepairPayload{OperationID: r.OperationID, ServerID: r.ServerID, RepoID: r.RepoID, Strategy: "abandon"}); err == nil {
		t.Fatal("duplicate repair accepted during admission")
	}
	p.localIntentMu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stored, _ = p.local.Get(r.OperationID)
	if stored.State != localrepo.StateAbandoned || stub.infoCalls != 1 || stub.checkout != 0 || len(p.attachments) != 0 || len(p.queue) != 0 || p.creationWaiters.Load() != 0 {
		t.Fatal("wrong execution or leaked priority")
	}
	for path, text := range map[string]string{filepath.Join(".svn", "wc.db"): "incomplete metadata", "drawing.dwg": "local edits"} {
		if b, err := os.ReadFile(filepath.Join(r.LocalPath, path)); err != nil || string(b) != text {
			t.Fatal("file changed", path, err)
		}
	}
	if s := state.Summary(); s.LifecycleOperationID != "" || s.CanAbandonLifecycle {
		t.Fatal("completed action still offered")
	}
}

func TestRepairAdmissionRechecksAndCancelsWithoutExecution(t *testing.T) {
	for _, mode := range []string{"state changed", "new attachment", "pending mutation", "profile withdrawn", "cancel", "shutdown", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			p, service, r, stub := repairAdmissionFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			shutdown, stop := context.WithCancel(t.Context())
			defer stop()
			service.anchorAdmissionCtx = shutdown
			var calls atomic.Int32
			service.onRepair = func(ctx context.Context, id, strategy string) (localrepo.Record, error) {
				calls.Add(1)
				return p.RepairLifecycle(ctx, id, strategy)
			}
			p.localIntentMu.Lock()
			locked := true
			defer func() {
				if locked {
					p.localIntentMu.Unlock()
				}
			}()
			if mode == "deadline" {
				var end context.CancelFunc
				ctx, end = context.WithTimeout(ctx, 100*time.Millisecond)
				defer end()
			}
			done := make(chan error, 1)
			go func() { _, err := service.Repair(ctx, r.OperationID, r.ServerID, r.RepoID, "abandon"); done <- err }()
			waitRepairAdmission(t, p, done)
			switch mode {
			case "state changed":
				if _, err := p.local.Retry(r.OperationID); err != nil {
					t.Fatal(err)
				}
			case "new attachment":
				if _, _, err := p.local.EnsureConfiguredAttached(r.ServerID, r.RepoID, r.RepoURL, "rw", filepath.Join(t.TempDir(), "other"), "New"); err != nil {
					t.Fatal(err)
				}
			case "pending mutation":
				if err := os.MkdirAll(filepath.Join(r.LocalPath, ".filees", "state"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(r.LocalPath, ".filees", "state", "remote-tree.pending.json"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "profile withdrawn":
				p.mu.Lock()
				delete(p.profiles, r.ServerID)
				p.mu.Unlock()
			case "cancel":
				cancel()
			case "shutdown":
				stop()
			}
			if mode != "deadline" {
				p.localIntentMu.Unlock()
				locked = false
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("changed/canceled operation executed")
				}
				if (mode == "cancel" || mode == "shutdown") && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation was hidden", err)
				}
				if mode == "deadline" && !errors.Is(err, commit.ErrLocalStatusBusy) {
					t.Fatal("admission timeout was hidden", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("wait did not stop")
			}
			if locked {
				p.localIntentMu.Unlock()
				locked = false
			}
			if mode != "profile withdrawn" && calls.Load() != 0 {
				t.Fatal("stale operation reached executor")
			}
			stored, _ := p.local.Get(r.OperationID)
			if stored.State == localrepo.StateAbandoned || stub.infoCalls != 0 || p.creationWaiters.Load() != 0 {
				t.Fatal("side effect or leaked waiter")
			}
			if !p.localIntentMu.TryLock() {
				t.Fatal("mutex leaked")
			}
			p.localIntentMu.Unlock()
		})
	}
}
