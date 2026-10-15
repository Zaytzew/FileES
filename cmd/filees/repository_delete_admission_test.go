package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
)

func TestDeleteAdmissionWaitsAndRevalidates(t *testing.T) {
	for _, mode := range []string{"remote", "attachment changed", "permission revoked", "canceled", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			mu.Lock()
			var waiters atomic.Int32
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			shutdown, stop := context.WithCancel(t.Context())
			defer stop()
			var allowed atomic.Bool
			allowed.Store(true)
			var executions atomic.Int32
			service := repositoryLifecycleService{store: store, localIntentMu: &mu, anchorAdmissionCtx: shutdown, anchorAdmissionWaiters: &waiters,
				deleteAdmissionCheck: func(string, string) error {
					if !allowed.Load() {
						return errors.New("permission revoked")
					}
					return nil
				},
				onDetach: func(_ context.Context, id string) (localrepo.Record, error) {
					executions.Add(1)
					r, _ := store.Get(id)
					return r, nil
				},
			}
			done := make(chan error, 1)
			go func() { _, err := service.BeginDelete(ctx, "office", "repo", "Docs"); done <- err }()
			deadline := time.Now().Add(2 * time.Second)
			for waiters.Load() == 0 {
				if time.Now().After(deadline) {
					mu.Unlock()
					t.Fatal("missing admission waiter")
				}
				time.Sleep(time.Millisecond)
			}
			if len(store.List()) != 0 || executions.Load() != 0 {
				mu.Unlock()
				t.Fatal("deletion started while fence held")
			}
			switch mode {
			case "attachment changed":
				_, _, err = store.EnsureConfiguredAttached("office", "repo", "svn+ssh://example/repo", "rw", filepath.Join(t.TempDir(), "wc"), "Docs")
				if err != nil {
					mu.Unlock()
					t.Fatal(err)
				}
			case "permission revoked":
				allowed.Store(false)
			case "canceled":
				cancel()
			case "shutdown":
				stop()
			}
			mu.Unlock()
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("admission did not complete")
			}
			if (err == nil) != (mode == "remote") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if (executions.Load() == 1) != (mode == "remote") || waiters.Load() != 0 {
				t.Fatal("wrong execution count or leaked priority")
			}
			for _, r := range store.List() {
				if mode != "remote" && r.State == localrepo.StateDeleting {
					t.Fatal("recorded deletion without admission")
				}
			}
			if !mu.TryLock() {
				t.Fatal("mutex leaked")
			}
			mu.Unlock()
		})
	}
}

func TestDeleteAdmissionDeadlineHasNoEffect(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
	service := repositoryLifecycleService{store: store, localIntentMu: &mu}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer cancel()
	if _, err := service.BeginDelete(ctx, "office", "repo", "Docs"); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatal("timeout persisted deletion")
	}
}

func TestDeleteAfterDetachedCopyUsesRemoteOnlyRecord(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := store.EnsureConfiguredAttached("office", "repo", "svn+ssh://example/repo", "rw", filepath.Join(t.TempDir(), "wc"), "Docs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginDetach("office", "repo", false); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteDetach(old.OperationID); err != nil {
		t.Fatal(err)
	}
	service := repositoryLifecycleService{store: store, onDetach: func(_ context.Context, id string) (localrepo.Record, error) { r, _ := store.Get(id); return r, nil }}
	var result contract.RepoLifecycleResult
	result, err = service.BeginDelete(t.Context(), "office", "repo", "Docs")
	if err != nil || result.State != "deleting" || result.LocalPath != "" || result.OperationID == old.OperationID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
