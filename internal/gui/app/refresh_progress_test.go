package app

import (
	"context"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

func TestRefreshProgressCountsOnlyNewObservationsWithoutAuthority(t *testing.T) {
	s := newAppState().applyConnected([]string{contract.CapRepoDelete})
	s = s.startPendingAction(PendingAction{ID: "delete:b", Kind: "delete"}).awaitPendingAction("delete:b")
	s = s.applySnapshot(contract.RepoStatus{RepoID: "b", Pending: contract.PendingStats{Added: 900}})
	s = s.applyRefreshInventory(contract.SystemStatusResult{}, []contract.RepoSummary{
		{ID: "a", ServerID: "lab"}, {ID: "b", ServerID: "lab"},
	})
	vm := s.viewModel()
	if vm.QueueScan == nil || vm.QueueScan.Files != 0 || vm.QueueScan.Total != 2 || !vm.Stale || vm.CanDeleteRepository() || len(vm.PendingActions) != 1 {
		t.Fatalf("inventory granted authority or included cached counts: %+v", vm)
	}
	s = s.applyRefreshProgress([]contract.RepoStatus{{RepoID: "a", Pending: contract.PendingStats{Renamed: 2, TotalBytes: 30}}})
	first := s.viewModel()
	if *first.QueueScan != (QueueScan{Observed: 1, Total: 2, Files: 2, Bytes: 30}) || !first.Stale {
		t.Fatalf("first batch: %+v", first.QueueScan)
	}
	s = s.applyRefreshProgress([]contract.RepoStatus{{RepoID: "a", Pending: contract.PendingStats{Modified: 1, TotalBytes: 10}}, {RepoID: "b"}})
	if got := s.viewModel().QueueScan; *got != (QueueScan{Observed: 2, Total: 2, Files: 1, Bytes: 10}) {
		t.Fatalf("duplicates must replace, not accumulate: %+v", got)
	}
	if first.QueueScan.Files != 2 || vm.QueueScan.Observed != 0 {
		t.Fatal("later progress mutated a published model")
	}
	if got := s.applyDisconnected().viewModel(); got.QueueScan != nil || !got.Stale {
		t.Fatalf("disconnected progress remained live: %+v", got.QueueScan)
	}
}

func TestRoutineRefreshDoesNotRestartInitialQueueIndicator(t *testing.T) {
	s := newAppState().applyConnected(nil)
	s.stale = false // A full snapshot of this session was already received.
	s = s.applyRefreshInventory(contract.SystemStatusResult{}, []contract.RepoSummary{{ID: "a", ServerID: "lab"}})
	s = s.applyRefreshProgress([]contract.RepoStatus{{RepoID: "a", Pending: contract.PendingStats{Added: 3}}})
	vm := s.viewModel()
	if vm.Stale || vm.QueueScan != nil || vm.Repos[0].Pending.Added != 3 {
		t.Fatalf("routine refresh flickered or lost data: %+v", vm.QueueScan)
	}
}

func TestRefreshProgressArrivesBeforeSlowRepositoryAndSupplementalLists(t *testing.T) {
	releaseSecond, releaseErrors := make(chan struct{}), make(chan struct{})
	d := &fakeDaemon{
		repoList: func(context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "a", ServerID: "lab"}, {ID: "b", ServerID: "lab"}}}, nil
		},
		repoStatus: func(ctx context.Context, id string) (*contract.RepoStatus, error) {
			if id == "b" {
				select {
				case <-releaseSecond:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &contract.RepoStatus{RepoID: id, ServerID: "lab", Pending: contract.PendingStats{Added: 1, TotalBytes: 20}}, nil
		},
		errorList: func(ctx context.Context, _ contract.ErrorListPayload) (*contract.ErrorListResult, error) {
			select {
			case <-releaseErrors:
				return &contract.ErrorListResult{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	application := startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})
	first := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.QueueScan != nil && vm.QueueScan.Observed == 1 })
	if !first.Stale || first.QueueScan.Files != 1 {
		t.Fatalf("first repository: %+v", first.QueueScan)
	}
	close(releaseSecond)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.QueueScan != nil && vm.QueueScan.Observed == 2 && vm.Stale })
	// Old-session progress must not affect the visible count, even while the
	// current session waits for supplementary data.
	application.msgCh <- msgRefreshProgress{gen: 0, statuses: []contract.RepoStatus{{RepoID: "a", Pending: contract.PendingStats{Added: 999}}}}
	close(releaseErrors)
	final := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && !vm.Stale && vm.QueueScan == nil })
	if len(final.Repos) != 2 || final.Repos[0].Pending.Added != 1 {
		t.Fatalf("final snapshot: %+v", final.Repos)
	}
}
