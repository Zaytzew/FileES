package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
)

type fixedReservationFetcher struct {
	result reservationv1.Result
	err    error
}

func (fetcher *fixedReservationFetcher) Fetch(context.Context, string) (reservationv1.Result, error) {
	return fetcher.result, fetcher.err
}

type projectionStatusClient struct {
	client.Client
	entries []client.StatusEntry
	err     error
}

func (svn *projectionStatusClient) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	return append([]client.StatusEntry(nil), svn.entries...), svn.err
}

func testReservationResult(repoID string) reservationv1.Result {
	return reservationv1.Result{
		Schema: reservationv1.Schema, RepoID: repoID,
		Reservations: []reservationv1.Reservation{{Path: "plans/a.dwg", Token: "token-1", OwnerID: "owner", CreatedAt: "2026-09-01T12:00:00Z"}},
		AsOf:         time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Generation: "7",
	}
}

func TestReservationProjectionListsDetachedRepoAndAppliesLocalOverlay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := ipcserver.New(t.TempDir() + "/daemon.sock")
	repoID := "cd591bfe-7270-5e0b-b2db-c9bf46aebe4a"
	server.RegisterProjectedRepo(repoID, "Plans", "svn+ssh://host/plans", "lab", contract.AccessReadWrite, "active", false)
	coordinator := newReservationProjectionCoordinator(ctx, server)
	defer coordinator.Close()
	fetcher := &fixedReservationFetcher{result: testReservationResult(repoID)}
	coordinator.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return fetcher, nil }
	view := clientview.View{Repositories: []clientview.Repository{{RepoID: repoID, State: "active"}}}
	coordinator.mu.Lock()
	coordinator.profiles["lab"] = clientprofile.Profile{ServerID: "lab"}
	coordinator.views["lab"] = view
	coordinator.mu.Unlock()
	coordinator.wireView("lab", view)
	coordinator.refresh(ctx, "lab")

	state := server.RepoState("lab", repoID)
	remote, err := state.ListReservations(ctx)
	if err != nil || len(remote.Reservations) != 1 {
		t.Fatalf("detached snapshot=%+v err=%v", remote, err)
	}
	if remote.Reservations[0].WorkingCopy != "" || remote.Reservations[0].CanRelease {
		t.Fatalf("detached overlay leaked local authority: %+v", remote.Reservations[0])
	}

	key := reposupervisor.Key{ServerID: "lab", RepoID: repoID}
	coordinator.AttachLocal(key, &projectionStatusClient{entries: []client.StatusEntry{{Path: "plans/a.dwg", Item: "modified", Props: "normal"}}}, "/work/plans", nil)
	local, err := state.ListReservations(ctx)
	if err != nil || len(local.Reservations) != 1 {
		t.Fatalf("local snapshot=%+v err=%v", local, err)
	}
	row := local.Reservations[0]
	if row.WorkingCopy != "/work/plans" || !row.CanRelease || !row.LocalChanges {
		t.Fatalf("local overlay=%+v", row)
	}
}

func TestReservationProjectionRetainsKnownRowsWhenStateLaneGoesOffline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := ipcserver.New(t.TempDir() + "/daemon.sock")
	repoID := "cd591bfe-7270-5e0b-b2db-c9bf46aebe4a"
	server.RegisterProjectedRepo(repoID, "Plans", "svn+ssh://host/plans", "lab", contract.AccessReadOnly, "active", false)
	coordinator := newReservationProjectionCoordinator(ctx, server)
	defer coordinator.Close()
	fetcher := &fixedReservationFetcher{result: testReservationResult(repoID)}
	coordinator.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return fetcher, nil }
	view := clientview.View{Repositories: []clientview.Repository{{RepoID: repoID, State: "active"}}}
	coordinator.mu.Lock()
	coordinator.profiles["lab"] = clientprofile.Profile{ServerID: "lab"}
	coordinator.views["lab"] = view
	coordinator.mu.Unlock()
	coordinator.wireView("lab", view)
	coordinator.refresh(ctx, "lab")
	fetcher.err = errors.New("network down")
	coordinator.refresh(ctx, "lab")

	snapshot, err := server.RepoState("lab", repoID).ListReservations(ctx)
	if err != nil || !snapshot.Offline || snapshot.Unknown || len(snapshot.Reservations) != 1 || snapshot.Generation != "7" {
		t.Fatalf("offline snapshot=%+v err=%v", snapshot, err)
	}
}
