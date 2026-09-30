package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/reposupervisor"
	v1 "filees/pkg/reservation/v1"
)

type storageFetcher struct {
	*fixedReservationFetcher
	calls int
}

func (f *storageFetcher) FetchAutolockStorage(context.Context, string) (v1.Result, error) {
	f.calls++
	return f.result, f.err
}

func TestStoragePublicationPersistsAcrossCoordinatorRestart(t *testing.T) {
	key := reposupervisor.Key{ServerID: "lab", RepoID: "cd591bfe-7270-5e0b-b2db-c9bf46aebe4a"}
	profile := clientprofile.Profile{ServerID: key.ServerID, CachePath: filepath.Join(t.TempDir(), "view.json")}
	now := time.Now()
	f := &storageFetcher{fixedReservationFetcher: &fixedReservationFetcher{result: v1.Result{Schema: v1.AutolockSchema, RepoID: key.RepoID, RepositoryState: "active", StorageWrite: &v1.StorageWrite{State: "blocked", MeasuredAt: &now, ValidForSeconds: 180}}}}
	makeCoordinator := func() *reservationProjectionCoordinator {
		c := newReservationProjectionCoordinator(t.Context(), nil)
		t.Cleanup(c.Close)
		c.profiles[key.ServerID] = profile
		c.views[key.ServerID] = clientview.View{Repositories: []clientview.Repository{{RepoID: key.RepoID, State: "active"}}}
		c.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return f, nil }
		return c
	}
	c := makeCoordinator()
	if err := c.CheckStorage(t.Context(), key); err != nil {
		t.Fatal("unconfigured server blocked", err)
	}
	c.refresh(t.Context(), key.ServerID)
	if f.calls != 1 || c.CheckStorage(t.Context(), key) == nil {
		t.Fatal("signal did not block")
	}
	c.Close()
	c = makeCoordinator()
	if c.CheckStorage(t.Context(), key) == nil {
		t.Fatal("restart forgot hold")
	}
	f.err = errors.New("network down")
	c.refresh(t.Context(), key.ServerID)
	if c.CheckStorage(t.Context(), key) == nil {
		t.Fatal("offline cleared hold")
	}
	f.err = nil
	f.result.StorageWrite = &v1.StorageWrite{State: "unknown"}
	c.refresh(t.Context(), key.ServerID)
	if c.CheckStorage(t.Context(), key) == nil {
		t.Fatal("unknown cleared hold")
	}
	f.result.StorageWrite = &v1.StorageWrite{State: "available", MeasuredAt: &now, ValidForSeconds: 180}
	c.refresh(t.Context(), key.ServerID)
	if err := c.CheckStorage(t.Context(), key); err != nil {
		t.Fatal("fresh recovery did not release", err)
	}
}
