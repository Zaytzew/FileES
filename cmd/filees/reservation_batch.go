package main

import (
	"context"
	"errors"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationclient"
)

type reservationBatchFetcher interface {
	FetchBatch(context.Context, reservationv1.BatchRequest) (reservationv1.BatchResponse, error)
}

// One refresh owns this cursor. Only the current page is retained in addition
// to the existing cache; errors stop this stream instead of opening N retries.
type reservationBatchReader struct {
	fetcher      reservationBatchFetcher
	profile      clientprofile.Profile
	view         clientview.View
	req          reservationv1.BatchRequest
	cached       map[string]reservationv1.Result
	page         map[string]reservationv1.Result
	started      time.Time
	loaded, done bool
	err          error
}

func (c *reservationProjectionCoordinator) batchReader(fetcher reservationFetcher, profile clientprofile.Profile, view clientview.View) *reservationBatchReader {
	f, ok := fetcher.(reservationBatchFetcher)
	if !ok {
		return nil
	}
	c.mu.RLock()
	if time.Now().Before(c.batchLegacyUntil[profile.ServerID]) {
		c.mu.RUnlock()
		return nil
	}
	r := &reservationBatchReader{fetcher: f, profile: profile, view: view, cached: map[string]reservationv1.Result{}, req: reservationv1.BatchRequest{Schema: reservationv1.BatchSchema, Scope: reservationv1.BatchScope(profile.ServerID, view), Known: map[string]string{}}}
	for key, cache := range c.results {
		if key.ServerID == profile.ServerID && cache.present && cache.profileEpoch == c.profileEpochs[profile.ServerID] && cache.result.Schema == reservationv1.AutolockSchema && cache.result.RepositoryState == "active" {
			r.cached[key.RepoID] = cache.result
		}
	}
	c.mu.RUnlock()
	// Hash potentially large immutable payloads outside the coordinator lock.
	for id, result := range r.cached {
		r.req.Known[id] = reservationv1.BatchVersion(r.req.Scope, result)
	}
	return r
}

func (r *reservationBatchReader) fetch(ctx context.Context, repoID string) (reservationv1.Result, time.Time, error) {
	for {
		if err := ctx.Err(); err != nil {
			return reservationv1.Result{}, time.Time{}, err
		}
		if r.err != nil {
			return reservationv1.Result{}, time.Time{}, r.err
		}
		if result, ok := r.page[repoID]; ok {
			return result, r.started, nil
		}
		if r.done {
			return reservationv1.Result{}, time.Time{}, errors.New("repository absent from authenticated state inventory")
		}
		r.started = time.Now()
		response, err := r.fetcher.FetchBatch(ctx, r.req)
		if errors.Is(err, reservationclient.ErrBatchUnsupported) && r.loaded {
			err = errors.New("reservation batch support lost during pagination")
		}
		if err == nil {
			r.page, err = reservationv1.ApplyBatch(r.req, response, r.profile.ServerID, r.profile.ClientID, r.view.RealmID, r.cached)
		}
		if err != nil {
			r.err = err
			continue
		}
		r.loaded, r.done = true, response.Next == ""
		r.req.After = response.Next
		// Request versions remain the immutable baseline of this pass. Scope
		// and strict cursor validation prevent loops or mixed authority pages.
	}
}
