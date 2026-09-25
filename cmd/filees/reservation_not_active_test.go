package main

import (
	"context"
	"testing"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
)

// The state lane asks only about repositories the view calls active. A new
// repository in its first publication is listed in another state; its
// snapshot says so instead of "unknown", which made the lock tile "0+?"
// throughout the initial import (Windows Sandbox, 2026-09-24).
func TestReservationSnapshotOfARepositoryNotYetActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := ipcserver.New(t.TempDir() + "/daemon.sock")
	creating := "0b6f3a8e-3a4c-5c3e-9f20-8a1f4e2d7c11"
	active := "cd591bfe-7270-5e0b-b2db-c9bf46aebe4a"
	coordinator := newReservationProjectionCoordinator(ctx, server)
	defer coordinator.Close()
	fetcher := &fixedReservationFetcher{result: testReservationResult(active)}
	coordinator.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return fetcher, nil }
	view := clientview.View{Repositories: []clientview.Repository{{RepoID: creating, State: "creating"}, {RepoID: active, State: "active"}}}
	coordinator.mu.Lock()
	coordinator.profiles["lab"] = clientprofile.Profile{ServerID: "lab"}
	coordinator.views["lab"] = view
	coordinator.mu.Unlock()

	snapshot, err := coordinator.snapshot(ctx, reposupervisor.Key{ServerID: "lab", RepoID: creating})
	if err != nil || !snapshot.NotActive || snapshot.Unknown {
		t.Fatalf("repository in its first publication: %+v, %v", snapshot, err)
	}
	// An active repository that has not answered yet stays unknown.
	snapshot, err = coordinator.snapshot(ctx, reposupervisor.Key{ServerID: "lab", RepoID: active})
	if err != nil || snapshot.NotActive || !snapshot.Unknown {
		t.Fatalf("unanswered active repository: %+v, %v", snapshot, err)
	}
	coordinator.refresh(ctx, "lab")
	snapshot, err = coordinator.snapshot(ctx, reposupervisor.Key{ServerID: "lab", RepoID: active})
	if err != nil || snapshot.NotActive || snapshot.Unknown || len(snapshot.Reservations) != 1 {
		t.Fatalf("answered active repository: %+v, %v", snapshot, err)
	}
}
