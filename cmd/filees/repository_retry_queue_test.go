package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

func retryQueueFixture(t *testing.T) (*daemonProvisioner, repositoryLifecycleService, localrepo.Record) {
	t.Helper()
	local, journal, profile, record := failedCreationRepairFixture(t)
	p := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	s := repositoryLifecycleService{store: local, localIntentMu: &p.localIntentMu, onRepair: p.RepairLifecycle, onRetryCreate: p.queueInitialImportRetry}
	return p, s, record
}

func TestImportRetryAcceptedBehindBusyObserver(t *testing.T) {
	p, s, r := retryQueueFixture(t)
	p.localIntentMu.Lock()
	defer p.localIntentMu.Unlock()
	for range 2 {
		got, err := s.Repair(t.Context(), r.OperationID, r.ServerID, r.RepoID, "retry")
		if err != nil {
			t.Fatal(err)
		}
		if got.OperationID != r.OperationID || got.RepoID != r.RepoID || got.LastError != "" {
			t.Fatalf("lost intent: %+v", got)
		}
	}
	select {
	case got := <-p.queue:
		if got != r.OperationID {
			t.Fatal(got)
		}
	default:
		t.Fatal("retry not queued")
	}
	select {
	case <-p.queue:
		t.Fatal("duplicate wakeup")
	default:
	}
	stored, _ := p.local.Get(r.OperationID)
	if stored.State != localrepo.StateRepositoryCreated || stored.LastError != "" {
		t.Fatal(stored)
	}
}

func TestImportRetryWaitsAndPreservesIntentOnShutdown(t *testing.T) {
	p, s, r := retryQueueFixture(t)
	p.localIntentMu.Lock()
	_, err := s.Repair(t.Context(), r.OperationID, r.ServerID, r.RepoID, "retry")
	if err != nil {
		p.localIntentMu.Unlock()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { p.runOne(ctx, r.OperationID); close(done) }()
	waitCreationQueued(t, p)
	select {
	case <-done:
		t.Fatal("crossed busy observer")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown stalled")
	}
	p.localIntentMu.Unlock()
	stored, _ := p.local.Get(r.OperationID)
	if stored.LastError != "" || stored.State != localrepo.StateRepositoryCreated {
		t.Fatal(stored)
	}
	if p.creationWaiters.Load() != 0 {
		t.Fatal("leaked waiter")
	}
	// Reopen both journals, not just the scheduler: restart must preserve retry.
	root := filepath.Dir(r.LocalPath)
	local, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(root, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := newDaemonProvisioner(local, journal, []clientprofile.Profile{p.profiles[r.ServerID]})
	if !restarted.restoreOperations(t.Context()) {
		t.Fatal("restore refused")
	}
	select {
	case id := <-restarted.queue:
		if id != r.OperationID {
			t.Fatal(id)
		}
	default:
		t.Fatal("lost durable retry")
	}
	if _, _, err := restarted.prepareQueuedCreation(t.Context(), r.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestImportRetryDoesNotReplayNewFailureAfterWaiting(t *testing.T) {
	p, s, r := retryQueueFixture(t)
	if _, err := s.Repair(t.Context(), r.OperationID, r.ServerID, r.RepoID, "retry"); err != nil {
		t.Fatal(err)
	}
	p.localIntentMu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := p.prepareQueuedCreation(t.Context(), r.OperationID); done <- err }()
	waitCreationQueued(t, p)
	_, err := p.local.MarkError(r.OperationID, errors.New("new transport failure"))
	if err != nil {
		t.Fatal(err)
	}
	p.localIntentMu.Unlock()
	if err := <-done; !errors.Is(err, errCreationIntentEnded) {
		t.Fatalf("new failure replayed: %v", err)
	}
	p.runOne(t.Context(), r.OperationID) // stale wakeup must stay inert
	stored, _ := p.local.Get(r.OperationID)
	if stored.LastError != "new transport failure" {
		t.Fatal(stored)
	}
}

func TestImportRetryAdmissionKeepsOtherGuards(t *testing.T) {
	p, s, r := retryQueueFixture(t)
	p.localIntentMu.Lock()
	defer p.localIntentMu.Unlock()
	waitCtx, endWait := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer endWait()
	if _, err := s.Repair(waitCtx, r.OperationID, r.ServerID, r.RepoID, "abandon"); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	if _, err := s.Repair(t.Context(), r.OperationID, "other", r.RepoID, "retry"); err == nil {
		t.Fatal("foreign target admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Repair(ctx, r.OperationID, r.ServerID, r.RepoID, "retry"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stored, _ := p.local.Get(r.OperationID)
	if stored.LastError != r.LastError {
		t.Fatal("refusal changed intent")
	}
}

func TestImportRetryIPCAdmitsWhileWriterBusy(t *testing.T) {
	p, service, r := retryQueueFixture(t)
	p.localIntentMu.Lock()
	defer p.localIntentMu.Unlock()
	socket := shortDaemonSocket(t)
	server := ipcserver.New(socket)
	server.SetRepositoryLifecycleService(service)
	state := server.RegisterProjectedRepo(r.RepoID, "Fixture", r.RepoURL, r.ServerID, "rw", contract.StateInteractionRequired, false)
	state.SetLifecycleRepairMetadata(r.OperationID, r.LastError, true, true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client := ipcclient.New(socket, "gui")
	result, err := client.RepoLifecycleRepair(ctx, contract.RepoLifecycleRepairPayload{OperationID: r.OperationID, ServerID: r.ServerID, RepoID: r.RepoID, Strategy: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationID != r.OperationID || result.LastError != "" {
		t.Fatalf("retry not acknowledged: %+v", result)
	}
	select {
	case id := <-p.queue:
		if id != r.OperationID {
			t.Fatal(id)
		}
	default:
		t.Fatal("IPC acknowledged without queueing")
	}
}

func TestImportRetryDuplicateDuringExecutionDoesNotArmAnotherAttempt(t *testing.T) {
	p, s, r := retryQueueFixture(t)
	p.running = map[string]provisionerOperation{r.OperationID: {}}
	if _, err := s.Repair(t.Context(), r.OperationID, r.ServerID, r.RepoID, "retry"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.queue:
		t.Fatal("duplicate running retry queued")
	default:
	}
	stored, _ := p.local.Get(r.OperationID)
	if stored.LastError != r.LastError {
		t.Fatal("running attempt's error erased")
	}
}

func TestImportRetryAbandonedWhileQueuedDoesNotExecute(t *testing.T) {
	p, s, r := retryQueueFixture(t)
	if _, err := s.Repair(t.Context(), r.OperationID, r.ServerID, r.RepoID, "retry"); err != nil {
		t.Fatal(err)
	}
	p.localIntentMu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := p.prepareQueuedCreation(t.Context(), r.OperationID); done <- err }()
	waitCreationQueued(t, p)
	_, err := p.local.Abandon(r.OperationID)
	p.localIntentMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, errCreationIntentEnded) {
		t.Fatalf("abandoned intent executed: %v", err)
	}
	p.runOne(t.Context(), r.OperationID)
	if _, err := p.queueInitialImportRetry(t.Context(), r); !errors.Is(err, errCreationIntentEnded) {
		t.Fatalf("stale retry resurrected abandoned intent: %v", err)
	}
}
