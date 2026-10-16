package main

import (
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
	"path/filepath"
	"testing"
	"time"
)

func TestReleaseACKDoesNotRemoveNewerTokenOrFreshenCache(t *testing.T) {
	c, f := batchCoordinatorFixture(t, 1)
	c.scheduler.Close()
	c.refresh(t.Context(), "lab")
	key := reposupervisor.Key{ServerID: "lab", RepoID: f.view.Repositories[0].RepoID}
	r := c.results[key]
	r.result.Reservations = []reservationv1.Reservation{{Path: "doc", Token: "new"}, {Path: "other", Token: "other-token"}}
	r.invalidated = true
	c.results[key] = r
	wc := t.TempDir()
	c.ObserveRelease(key, wc, filepath.Join(wc, "doc"), "old")
	if len(c.results[key].result.Reservations) != 2 {
		t.Fatal("newer token removed")
	}
	c.ObserveRelease(key, wc, filepath.Join(wc, "doc"), "new")
	got := c.results[key]
	if len(got.result.Reservations) != 1 || got.result.Reservations[0].Token != "other-token" || got.receivedAt != r.receivedAt || !got.invalidated {
		t.Fatal("ACK changed unrelated evidence")
	}
}

func TestReservationMutationFencesOldAndOverlappingReads(t *testing.T) {
	c, f := batchCoordinatorFixture(t, 2)
	c.scheduler.Close() // deterministic synchronous test; no background requests
	c.refresh(t.Context(), "lab")
	key := reposupervisor.Key{ServerID: "lab", RepoID: f.view.Repositories[0].RepoID}
	other := reposupervisor.Key{ServerID: "lab", RepoID: f.view.Repositories[1].RepoID}
	previous := c.results[key]
	f.onCall = func() { finish := c.BeginMutation(key); finish(); finish() }
	c.refresh(t.Context(), "lab")
	if !c.results[key].invalidated || !c.results[key].receivedAt.Equal(previous.receivedAt) {
		t.Fatal("late read accepted")
	}
	if c.results[other].invalidated {
		t.Fatal("unrelated repo invalidated")
	}
	f.onCall = nil
	finish := c.BeginMutation(key)
	c.refresh(t.Context(), "lab")
	if !c.results[key].invalidated {
		t.Fatal("overlapping read accepted")
	}
	if reason := ownershipCacheReason(config.Repo{ID: key.RepoID, RealmID: f.view.RealmID}, c.results[key], f.view, true, false, 0, time.Now()); reason != "observation_invalidated" {
		t.Fatal(reason)
	}
	finish()
	c.refresh(t.Context(), "lab")
	if c.results[key].invalidated {
		t.Fatal("fresh confirmation rejected")
	}
	if f.changed != 2 {
		t.Fatal("invalidation lost differential baseline", f.changed)
	}
}

func TestPublicLinksLegacyReadCannotOverwriteMutation(t *testing.T) {
	c := newPublicShareCache()
	c.Set("lab", []contract.PublicShareSummary{{ChannelID: "original"}})
	epoch := c.BeginRefresh("lab")
	_, finish, err := c.beginMutation(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if !c.Snapshot().Stale {
		t.Fatal("lost mutation ACK left inventory confirmed")
	}
	c.CompleteRefresh("lab", epoch, []contract.PublicShareSummary{{ChannelID: "old"}}, nil)
	if c.Snapshot().Shares[0].ChannelID != "original" {
		t.Fatal("legacy stale read accepted")
	}
	_, finish, err = c.beginMutation(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	epoch = c.BeginRefresh("lab")
	c.CompleteRefresh("lab", epoch, nil, nil)
	finish()
	c.CompleteRefresh("lab", epoch, nil, nil)
	if len(c.Snapshot().Shares) != 1 {
		t.Fatal("overlapping legacy read accepted")
	}
	epoch = c.BeginRefresh("lab")
	c.CompleteRefresh("lab", epoch, nil, nil)
	if len(c.Snapshot().Shares) != 0 {
		t.Fatal("new read rejected")
	}
}
