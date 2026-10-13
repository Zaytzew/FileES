package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

func queuedCreationFixture(t *testing.T, archive bool) (*daemonProvisioner, string) {
	t.Helper()
	root := t.TempDir()
	local, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := provisioning.NewStore(filepath.Join(root, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(local, store, []clientprofile.Profile{{ServerID: "office", ClientID: "client"}})
	service := repositoryLifecycleService{store: local, provisioning: store, clientID: p.ClientID, onCreate: p.Enqueue, localIntentMu: &p.localIntentMu}
	target := filepath.Join(root, "Project")
	// This is the live failure: unrelated background/transfer turn owns the
	// shared mutex while the operator has already supplied all parameters.
	p.localIntentMu.Lock()
	defer p.localIntentMu.Unlock()
	var id string
	if archive {
		source := filepath.Join(root, "source.svndump")
		if err := os.WriteFile(source, []byte("SVN-fs-dump-format-version: 2\n\nfixture"), 0600); err != nil {
			t.Fatal(err)
		}
		r, err := service.BeginCreateArchive("office", "Project", target, source)
		if err != nil {
			t.Fatal(err)
		}
		id = r.OperationID
	} else {
		r, err := service.BeginCreate("office", "Project", target)
		if err != nil {
			t.Fatal(err)
		}
		id = r.OperationID
	}
	select {
	case got := <-p.queue:
		if got != id {
			t.Fatal(got)
		}
	default:
		t.Fatal("intent not queued")
	}
	return p, id
}

func waitCreationQueued(t *testing.T, p *daemonProvisioner) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for p.creationWaiters.Load() == 0 {
		if time.Now().After(until) {
			t.Fatal("creation did not wait")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueuedCreationWaitsThenRevalidatesWithoutRecreating(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "archive"}[archive], func(t *testing.T) {
			p, id := queuedCreationFixture(t, archive)
			p.localIntentMu.Lock()
			done := make(chan error, 1)
			go func() { _, _, err := p.prepareQueuedCreation(t.Context(), id); done <- err }()
			waitCreationQueued(t, p)
			op, _ := p.provisioning.Get(id)
			if op.RepoID != "" || len(op.Requests) != 0 {
				t.Fatal("remote request before admission")
			}
			select {
			case err := <-done:
				t.Fatal("did not wait", err)
			default:
			}
			p.localIntentMu.Unlock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("did not resume")
			}
			if len(p.local.List()) != 1 || p.creationWaiters.Load() != 0 {
				t.Fatal("duplicate or leaked waiter")
			}
			if !p.localIntentMu.TryLock() {
				t.Fatal("preparation retained fence for transfer")
			}
			p.localIntentMu.Unlock()
		})
	}
}

func TestQueuedCreationShutdownPreservesDurableIntent(t *testing.T) {
	p, id := queuedCreationFixture(t, true)
	p.localIntentMu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { p.runOne(ctx, id); done <- nil }()
	waitCreationQueued(t, p)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown stalled")
	}
	p.localIntentMu.Unlock()
	record, _ := p.local.Get(id)
	if record.State != localrepo.StateRequestPending || record.LastError != "" {
		t.Fatal(record.State, record.LastError)
	}
	// Reopen the real journals, just as restart does, and admit the same ID.
	root := filepath.Dir(record.LocalPath)
	local, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := provisioning.NewStore(filepath.Join(root, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := newDaemonProvisioner(local, store, []clientprofile.Profile{{ServerID: "office", ClientID: "client"}})
	if !restarted.restoreOperations(t.Context()) {
		t.Fatal("restore refused")
	}
	select {
	case got := <-restarted.queue:
		if got != id {
			t.Fatal("restart changed operation ID")
		}
	default:
		t.Fatal("restart lost intent")
	}
	if _, _, err := restarted.prepareQueuedCreation(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedArchiveRefusesChangedTargetAfterWaiting(t *testing.T) {
	p, id := queuedCreationFixture(t, true)
	r, _ := p.local.Get(id)
	p.localIntentMu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := p.prepareQueuedCreation(t.Context(), id); done <- err }()
	waitCreationQueued(t, p)
	if err := os.MkdirAll(r.LocalPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.LocalPath, "keep"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	p.localIntentMu.Unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("changed target admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("did not finish")
	}
	op, _ := p.provisioning.Get(id)
	if op.RepoID != "" || len(op.Requests) != 0 {
		t.Fatal("changed target reached server")
	}
}

func TestQueuedCreationRefusesReboundActivation(t *testing.T) {
	p, id := queuedCreationFixture(t, true)
	p.AddProfile(clientprofile.Profile{ServerID: "office", ClientID: "other-client"})
	if _, _, err := p.prepareQueuedCreation(t.Context(), id); err == nil {
		t.Fatal("rebound activation admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := p.prepareQueuedCreation(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestQueuedCreationFullWakeupChannelRetriesWithoutRestart(t *testing.T) {
	p, id := queuedCreationFixture(t, true)
	// The fixture consumed the first wakeup, emulate the Run dequeue bookkeeping.
	p.mu.Lock()
	delete(p.queued, id)
	p.mu.Unlock()
	p.queue = make(chan string, 1)
	p.queue <- "other"
	p.Enqueue(id)
	<-p.queue
	p.requeuePendingCreations()
	p.requeuePendingCreations()
	select {
	case got := <-p.queue:
		if got != id {
			t.Fatal(got)
		}
	default:
		t.Fatal("full channel lost intent until restart")
	}
	select {
	case got := <-p.queue:
		t.Fatal("duplicate wakeup", got)
	default:
	}
}

func TestQueuedCreationDoesNotReviveEndedIntent(t *testing.T) {
	p, id := queuedCreationFixture(t, true)
	p.localIntentMu.Lock()
	done := make(chan struct{})
	go func() { p.runOne(t.Context(), id); close(done) }()
	waitCreationQueued(t, p)
	want := errors.New("independent terminal decision")
	if _, err := p.local.MarkError(id, want); err != nil {
		t.Fatal(err)
	}
	p.localIntentMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ended intent stalled")
	}
	r, _ := p.local.Get(id)
	if r.State != localrepo.StateError || r.LastError != want.Error() {
		t.Fatal("waiting work overwrote newer decision", r.State, r.LastError)
	}
	op, _ := p.provisioning.Get(id)
	if op.RepoID != "" || len(op.Requests) != 0 {
		t.Fatal("ended intent reached server")
	}
}
