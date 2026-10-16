package main

import (
	"testing"
	"time"

	"filees/pkg/clientview"
)

func TestPublicSharePollJoinsScopeButPreservesMutation(t *testing.T) {
	view := clientview.View{RealmID: "owner", Generation: 1, Repositories: []clientview.Repository{{RepoID: "repo", OwnerRealmID: "owner", State: "active"}}}
	lister := &blockingPublicShareLister{calls: make(chan string, 8), release: make(chan struct{})}
	cache := newPublicShareCache()
	c := newPublicShareRefreshCoordinator(t.Context(), lister, cache, nil)
	c.Poll("cloud", view)
	select {
	case <-lister.calls:
	case <-time.After(time.Second):
		t.Fatal("read not started")
	}
	for range 256 {
		c.Poll("cloud", view)
	}
	c.mu.Lock()
	_, pending := c.pending["cloud"]
	c.mu.Unlock()
	if pending {
		close(lister.release)
		t.Fatal("ticks queued redundant read")
	}
	changed := view
	changed.Generation++
	c.Schedule("cloud", changed)
	c.Poll("cloud", view) // old observation hint cannot overwrite a new mutation
	c.mu.Lock()
	next := c.pending["cloud"]
	c.mu.Unlock()
	close(lister.release)
	if next.Generation != changed.Generation {
		t.Fatal("hint overwrote pending invalidation")
	}
	select {
	case <-lister.calls:
	case <-time.After(time.Second):
		t.Fatal("mutation was not refreshed")
	}
	waitPublicShareIdle(t, c)
	select {
	case <-lister.calls:
		t.Fatal("extra pass")
	default:
	}
	c.Poll("cloud", changed)
	select {
	case <-lister.calls:
	case <-time.After(time.Second):
		t.Fatal("next tick was skipped")
	}
	waitPublicShareIdle(t, c)
}

func TestPublicSharePollWithDifferentScopeMustRerun(t *testing.T) {
	view := clientview.View{RealmID: "owner", Repositories: []clientview.Repository{{RepoID: "repo", OwnerRealmID: "owner", State: "active"}}}
	lister := &blockingPublicShareLister{calls: make(chan string, 8), release: make(chan struct{})}
	c := newPublicShareRefreshCoordinator(t.Context(), lister, newPublicShareCache(), nil)
	c.Poll("cloud", view)
	select {
	case <-lister.calls:
	case <-time.After(time.Second):
		t.Fatal("read not started")
	}
	view.ClientID = "new-activation"
	c.Poll("cloud", view)
	c.mu.Lock()
	next, pending := c.pending["cloud"]
	c.mu.Unlock()
	close(lister.release)
	if !pending || next.ClientID != view.ClientID {
		t.Fatal("different scope dropped")
	}
	waitPublicShareIdle(t, c)
}

func waitPublicShareIdle(t *testing.T, c *publicShareRefreshCoordinator) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		idle := len(c.running) == 0
		c.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("public share refresh did not finish")
}
