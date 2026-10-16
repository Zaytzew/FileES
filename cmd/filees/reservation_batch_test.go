package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/config"
	"filees/pkg/pathownership"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationclient"
	"github.com/google/uuid"
)

type batchTestFetcher struct {
	view                             clientview.View
	batchCalls, legacyCalls, changed int
	failAt                           int
	err                              error
	onCall                           func()
}

func (f *batchTestFetcher) Fetch(_ context.Context, id string) (reservationv1.Result, error) {
	f.legacyCalls++
	return f.result(id), nil
}
func (f *batchTestFetcher) result(id string) reservationv1.Result {
	return reservationv1.Result{Schema: reservationv1.AutolockSchema, RepoID: id, RepositoryState: "active", AsOf: time.Now(), Generation: "1", ViewGeneration: f.view.Generation, PathOwnership: &pathownership.Snapshot{RepositoryUUID: id, Revision: 1}}
}
func (f *batchTestFetcher) FetchBatch(_ context.Context, req reservationv1.BatchRequest) (reservationv1.BatchResponse, error) {
	f.batchCalls++
	if f.onCall != nil {
		f.onCall()
	}
	if f.err != nil && (f.failAt == 0 || f.batchCalls >= f.failAt) {
		return reservationv1.BatchResponse{}, f.err
	}
	ids := reservationv1.BatchInventory(f.view)
	page, next, err := reservationv1.BatchPage(ids, req.After)
	if err != nil {
		return reservationv1.BatchResponse{}, err
	}
	r := reservationv1.BatchResponse{Schema: reservationv1.BatchSchema, Scope: req.Scope, ServerID: "lab", ClientID: f.view.ClientID, RealmID: f.view.RealmID, After: req.After, Next: next, Repositories: ids}
	for _, id := range page {
		e := reservationv1.NewBatchEntry(req.Scope, f.result(id), req.Known[id])
		if e.Result != nil {
			f.changed++
		}
		r.Entries = append(r.Entries, e)
	}
	return r, nil
}

func batchCoordinatorFixture(t *testing.T, n int) (*reservationProjectionCoordinator, *batchTestFetcher) {
	t.Helper()
	c := newReservationProjectionCoordinator(t.Context(), nil)
	t.Cleanup(c.Close)
	v := clientview.View{ClientID: uuid.NewString(), RealmID: uuid.NewString(), Generation: 1}
	for i := 0; i < n; i++ {
		v.Repositories = append(v.Repositories, clientview.Repository{RepoID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), State: "active"})
	}
	f := &batchTestFetcher{view: v}
	c.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return f, nil }
	c.profiles["lab"] = clientprofile.Profile{ServerID: "lab", ClientID: v.ClientID, CachePath: t.TempDir()}
	c.views["lab"] = v
	return c, f
}

func TestReservationBatchCoordinatorInitDeltaAgeAndEpoch(t *testing.T) {
	c, f := batchCoordinatorFixture(t, 55)
	before := time.Now()
	c.refresh(t.Context(), "lab")
	if f.batchCalls != 14 || f.legacyCalls != 0 || len(c.results) != 55 || f.changed != 55 {
		t.Fatal(f.batchCalls, f.legacyCalls, len(c.results), f.changed)
	}
	key := reposupervisor.Key{ServerID: "lab", RepoID: f.view.Repositories[0].RepoID}
	first := c.results[key]
	if first.receivedAt.Before(before) || first.receivedAt.After(time.Now()) {
		t.Fatal("bad receipt clock")
	}
	if reason := ownershipCacheReason(config.Repo{ID: key.RepoID, RealmID: f.view.RealmID}, first, f.view, true, false, 0, first.receivedAt.Add(61*time.Second)); reason == "" {
		t.Fatal("batch extended TTL")
	}
	c.refresh(t.Context(), "lab")
	if f.batchCalls != 28 || f.changed != 55 {
		t.Fatal("unchanged payloads resent", f.batchCalls, f.changed)
	}
	f.onCall = func() { c.mu.Lock(); c.profileEpochs["lab"]++; c.mu.Unlock() }
	c.refresh(t.Context(), "lab")
	if c.results[key].profileEpoch != 0 {
		t.Fatal("old activation refreshed")
	}
}

func TestReservationBatchCoordinatorNoDowngradeOrFresheningOnFailure(t *testing.T) {
	for _, mode := range []string{"network", "unsupported", "partial", "partial-unsupported"} {
		t.Run(mode, func(t *testing.T) {
			c, f := batchCoordinatorFixture(t, 9)
			c.refresh(t.Context(), "lab")
			old := map[reposupervisor.Key]time.Time{}
			for key, r := range c.results {
				old[key] = r.receivedAt
			}
			f.batchCalls = 0
			f.err = errors.New("network down")
			if mode == "unsupported" || mode == "partial-unsupported" {
				f.err = reservationclient.ErrBatchUnsupported
			}
			if mode == "partial" || mode == "partial-unsupported" {
				f.failAt = 2
			}
			c.refresh(t.Context(), "lab")
			if mode == "unsupported" {
				if f.batchCalls != 1 || f.legacyCalls != 9 {
					t.Fatal(f.batchCalls, f.legacyCalls)
				}
				c.refresh(t.Context(), "lab")
				if f.batchCalls != 1 {
					t.Fatal("legacy probe repeated during backoff")
				}
				return
			}
			wantCalls := 1
			if f.failAt == 2 {
				wantCalls = 2
			}
			if f.batchCalls != wantCalls || f.legacyCalls != 0 {
				t.Fatal("retry storm", f.batchCalls, f.legacyCalls)
			}
			failed := 0
			for key, r := range c.results {
				if r.offline {
					failed++
					if !r.receivedAt.Equal(old[key]) {
						t.Fatal("failure refreshed cache")
					}
				}
			}
			wantFailed := 9
			if f.failAt == 2 {
				wantFailed = 5
			}
			if failed != wantFailed {
				t.Fatal(failed)
			}
		})
	}
}

func TestReservationBatchReaderUsesPageStartNotPerItemArrival(t *testing.T) {
	c, f := batchCoordinatorFixture(t, 4)
	r := c.batchReader(f, c.profiles["lab"], f.view)
	_, first, err := r.fetch(t.Context(), f.view.Repositories[0].RepoID)
	if err != nil {
		t.Fatal(err)
	}
	// Model waiting in processing the preceding items without sleeping a minute.
	r.started = r.started.Add(-time.Minute)
	_, next, err := r.fetch(t.Context(), f.view.Repositories[3].RepoID)
	if err != nil || !next.Equal(first.Add(-time.Minute)) || f.batchCalls != 1 {
		t.Fatal("receipt renewed on cache read", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err = r.fetch(ctx, f.view.Repositories[3].RepoID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
