package main

import (
	"context"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/config"
	"filees/pkg/pathownership"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
	"github.com/google/uuid"
)

type ownershipSVN struct {
	client.Client
	revision int64
	uuid     string
	onInfo   func()
}

func (c ownershipSVN) Revision(context.Context, string) (int64, error) { return c.revision, nil }
func (c ownershipSVN) GetInfo(context.Context, string) (string, error) {
	if c.onInfo != nil {
		c.onInfo()
	}
	return "Repository UUID: " + c.uuid + "\n", nil
}

func TestDaemonOwnershipRequiresFreshBoundBrokerState(t *testing.T) {
	c := newReservationProjectionCoordinator(t.Context(), nil)
	defer c.Close()
	repo := config.Repo{ID: uuid.NewString(), ServerID: "lab", RealmID: uuid.NewString(), RepoURL: "svn://example/repo", LocalPath: t.TempDir()}
	key := reposupervisor.Key{ServerID: repo.ServerID, RepoID: repo.ID}
	incarnation := uuid.NewString()
	c.views["lab"] = clientview.View{RealmID: repo.RealmID, Generation: 2}
	valid := cachedReservationResult{present: true, receivedAt: time.Now(), result: reservationv1.Result{Schema: reservationv1.AutolockSchema, RepoID: repo.ID, RepositoryState: "active", ViewGeneration: 2, PathOwnership: &pathownership.Snapshot{RepositoryUUID: incarnation, Revision: 3, Entries: []pathownership.Entry{{Path: "a", Object: pathownership.Object{Kind: "file"}, OwnerRealmID: repo.RealmID}}}}}
	svn := ownershipSVN{revision: 3, uuid: incarnation}
	for _, tc := range []struct {
		name    string
		change  func(*cachedReservationResult, *ownershipSVN)
		allowed bool
	}{
		{"fresh", func(*cachedReservationResult, *ownershipSVN) {}, true},
		{"offline", func(r *cachedReservationResult, _ *ownershipSVN) { r.offline = true }, false},
		{"old profile", func(r *cachedReservationResult, _ *ownershipSVN) { r.profileEpoch = 99 }, false},
		{"stale", func(r *cachedReservationResult, _ *ownershipSVN) { r.result.Stale = true }, false},
		{"expired", func(r *cachedReservationResult, _ *ownershipSVN) { r.receivedAt = time.Now().Add(-2 * time.Minute) }, false},
		{"old view", func(r *cachedReservationResult, _ *ownershipSVN) { r.result.ViewGeneration = 1 }, false},
		{"legacy", func(r *cachedReservationResult, _ *ownershipSVN) { r.result.Schema = reservationv1.Schema }, false},
		{"new commit", func(_ *cachedReservationResult, s *ownershipSVN) { s.revision = 4 }, false},
		{"different WC incarnation", func(_ *cachedReservationResult, s *ownershipSVN) { s.uuid = uuid.NewString() }, false},
		{"reactivation during SVN check", func(_ *cachedReservationResult, s *ownershipSVN) {
			s.onInfo = func() {
				c.mu.Lock()
				c.profileEpochs[repo.ServerID]++
				c.mu.Unlock()
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := valid, svn
			tc.change(&r, &s)
			c.mu.Lock()
			c.results[key] = r
			c.mu.Unlock()
			got, err := c.Ownership(t.Context(), repo, s)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
			if tc.allowed && got.Owners["a"] != repo.RealmID {
				t.Fatal(got)
			}
		})
	}
}

type delayedOwnershipFetcher struct {
	started chan struct{}
	release chan struct{}
	result  reservationv1.Result
}

func (f *delayedOwnershipFetcher) Fetch(context.Context, string) (reservationv1.Result, error) {
	close(f.started)
	<-f.release
	return f.result, nil
}

func TestOwnershipRejectsInflightResponseAcrossProfileABA(t *testing.T) {
	c := newReservationProjectionCoordinator(t.Context(), nil)
	defer c.Close()
	repoID := uuid.NewString()
	profile := clientprofile.Profile{ServerID: "lab", ClientID: "original"}
	c.profiles["lab"] = profile
	c.started["lab"] = true // drive refresh explicitly, no periodic goroutine
	c.views["lab"] = clientview.View{Repositories: []clientview.Repository{{RepoID: repoID, State: "active"}}}
	f := &delayedOwnershipFetcher{started: make(chan struct{}), release: make(chan struct{}), result: testReservationResult(repoID)}
	c.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return f, nil }
	done := make(chan struct{})
	go func() { defer close(done); c.refresh(t.Context(), "lab") }()
	<-f.started
	replacement := profile
	replacement.ClientID = "replacement"
	c.UpdateProfile(replacement)
	c.UpdateProfile(profile)
	close(f.release)
	<-done
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.results[reposupervisor.Key{ServerID: "lab", RepoID: repoID}].present {
		t.Fatal("previous activation repopulated the ownership cache")
	}
}
