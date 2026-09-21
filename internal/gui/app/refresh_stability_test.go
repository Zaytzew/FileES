package app

import (
	"context"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

// Exercise the real event loop with a deliberately blocked full refresh.
// Ordinary data events must refresh without invalidating unrelated presentation;
// missing events must still immediately close the mutation gates.
func TestAppDataRefreshPreservesFreshnessUnlessSequenceLost(t *testing.T) {
	for _, eventType := range []string{contract.EvPublicSharesChanged, contract.EvNoticeCreated, contract.EvLockReleaseChanged} {
		for _, gap := range []bool{false, true} {
			name := eventType + "/contiguous"
			if gap {
				name = eventType + "/gap"
			}
			t.Run(name, func(t *testing.T) {
				events := make(chan contract.Event, 4)
				entered := make(chan struct{}, 4)
				release := make(chan struct{})
				calls := 0 // Full refreshes are serialised by the runner.
				daemon := &fakeDaemon{
					subscribe: func(context.Context) (<-chan contract.Event, error) { return events, nil },
					systemStatus: func(ctx context.Context) (*contract.SystemStatusResult, error) {
						calls++
						if calls > 2 {
							entered <- struct{}{}
							select {
							case <-release:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
						return &contract.SystemStatusResult{UptimeSec: int64(calls), Activations: []contract.ActivationStatus{{ServerID: "s", ViewSyncedAt: "2026-09-21T10:00:00Z"}}}, nil
					},
					repoList: func(context.Context) (*contract.RepoListResult, error) {
						return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "r", ServerID: "s", Attached: true}}}, nil
					},
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				views := newVMCollector()
				startApp(ctx, daemon, views, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})
				views.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && !vm.Stale })
				// Establish a sequence baseline and wait for that full snapshot.
				events <- contract.Event{Sequence: 1, Type: contract.EvActivityChanged}
				before := views.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.UptimeSec == 2 })
				if !before.CanFoldInactive(before.Repos[0]) || !before.CanMutateLock() || !before.CanAttachRepository() {
					t.Fatal("fixture must begin with foldable repo and enabled actions")
				}
				sequence := int64(2)
				if gap {
					sequence = 3
				}
				events <- contract.Event{Sequence: sequence, Type: eventType, RepoID: "r"}
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("data event did not request a full snapshot")
				}
				if gap {
					stale := views.waitFor(t, time.Second, func(vm ViewModel) bool { return vm.Stale })
					if stale.CanMutateLock() || stale.CanMutateUnlock() || stale.CanAttachRepository() {
						t.Fatal("sequence loss left mutation gates open")
					}
				}
				close(release)
				// Inspect every emission until completion, not only the final view:
				// the regression was a short stale emission followed by a good one.
				after := views.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
					if !gap && (vm.Stale || !vm.Connected || !vm.CanFoldInactive(vm.Repos[0]) || !vm.CanMutateLock()) {
						t.Fatalf("ordinary refresh invalidated the view: connected=%t stale=%t", vm.Connected, vm.Stale)
					}
					return vm.UptimeSec >= 3 && !vm.Stale
				})
				if !after.CanFoldInactive(after.Repos[0]) || !after.CanMutateLock() {
					t.Fatal("completed snapshot did not restore the view")
				}
				// A real stream failure must still disable actions immediately.
				close(events)
				offline := views.waitFor(t, time.Second, func(vm ViewModel) bool { return !vm.Connected })
				if !offline.Stale || offline.CanMutateLock() || offline.CanMutateUnlock() || offline.CanAttachRepository() {
					t.Fatal("disconnect did not close mutation gates")
				}
			})
		}
	}
}
