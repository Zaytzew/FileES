package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"filees/internal/gui/projectionrefresh"
	"filees/pkg/clientview"
	"filees/pkg/reposupervisor"
)

func TestReservationViewHeartbeatIsNotInvalidation(t *testing.T) {
	base := clientview.View{Schema: clientview.Schema, ClientID: "client", RealmID: "realm", Generation: 7,
		GeneratedAt: time.Now(), ClientRole: "rw", Repositories: []clientview.Repository{{RepoID: "repo", State: "active", Access: "rw"}}}
	for _, tc := range []struct {
		name   string
		change func(*clientview.View)
		equal  bool
	}{
		{"unchanged", func(*clientview.View) {}, true},
		{"heartbeat", func(v *clientview.View) { v.GeneratedAt = v.GeneratedAt.Add(time.Minute) }, true},
		{"generation", func(v *clientview.View) { v.Generation++ }, false},
		{"activation", func(v *clientview.View) { v.ClientID = "other" }, false},
		{"realm", func(v *clientview.View) { v.RealmID = "other" }, false},
		{"role", func(v *clientview.View) { v.ClientRole = "ro" }, false},
		{"access", func(v *clientview.View) { v.Repositories[0].Access = "ro" }, false},
		{"removed", func(v *clientview.View) { v.Repositories = nil }, false},
		{"added", func(v *clientview.View) {
			v.Repositories = append(v.Repositories, clientview.Repository{RepoID: "new"})
		}, false},
		{"policy", func(v *clientview.View) { v.Repositories[0].EditingPolicy = clientview.EditingLockRequired }, false},
		{"digest", func(v *clientview.View) { v.Repositories[0].MetadataDigest = "changed" }, false},
		{"owner", func(v *clientview.View) { v.Repositories[0].OwnerRealmID = "other" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var next clientview.View
			if err := json.Unmarshal(raw, &next); err != nil {
				t.Fatal(err)
			}
			tc.change(&next)
			if sameReservationView(base, next) != tc.equal {
				t.Fatal("wrong scope comparison")
			}
		})
	}
}

func TestRepeatedReservationViewDoesNotRequeueOrRefreshEvidence(t *testing.T) {
	c := newReservationProjectionCoordinator(t.Context(), nil)
	c.scheduler.Close()
	started := make(chan struct{}, 4)
	release := make(chan struct{}, 4)
	c.scheduler = projectionrefresh.New(t.Context(), func(ctx context.Context, _ string) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	t.Cleanup(c.Close)
	view := clientview.View{ClientID: "client", RealmID: "realm", Generation: 1, Repositories: []clientview.Repository{{RepoID: "repo", State: "active"}}}
	key := reposupervisor.Key{ServerID: "cloud", RepoID: "repo"}
	observed := time.Now().Add(-2 * time.Minute)
	c.results[key] = cachedReservationResult{receivedAt: observed, present: true, offline: true}
	c.UpdateView("cloud", view)
	waitReservationRead(t, started)
	for range 100 {
		view.GeneratedAt = time.Now()
		c.UpdateView("cloud", view)
		c.Poll("cloud")
	}
	if got := c.results[key]; got.receivedAt != observed || !got.offline {
		t.Fatal("hint refreshed evidence")
	}
	release <- struct{}{}
	select {
	case <-started:
		t.Fatal("identical view or poll queued repeat")
	case <-time.After(50 * time.Millisecond):
	}
	// A different generation still invalidates; unchanged timestamp is irrelevant.
	view.Generation++
	c.UpdateView("cloud", view)
	waitReservationRead(t, started)
	c.Schedule("cloud") // own mutation must survive concurrent ordinary polls
	c.Poll("cloud")
	release <- struct{}{}
	waitReservationRead(t, started)
	// A replaced transport invalidates even an otherwise identical view.
	c.mu.Lock()
	c.profileEpochs["cloud"]++
	c.mu.Unlock()
	c.UpdateView("cloud", view)
	release <- struct{}{}
	waitReservationRead(t, started)
	c.pauseRemoved("cloud")
	c.Poll("cloud")
	release <- struct{}{}
	select {
	case <-started:
		t.Fatal("paused lane reopened")
	case <-time.After(50 * time.Millisecond):
	}
}

func waitReservationRead(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("read did not start")
	}
}
