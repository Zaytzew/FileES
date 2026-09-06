package main

import (
	"context"
	"errors"
	"time"

	"filees/pkg/client"
	"filees/pkg/config"
	"filees/pkg/errcat"
	"filees/pkg/passport"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
)

// Ownership reads only the existing broker lane's latest observation. It never
// opens another broker connection or revives an on-disk stale projection.
func (c *reservationProjectionCoordinator) Ownership(ctx context.Context, repo config.Repo, svn client.Client) (passport.OwnershipView, error) {
	unknown := func(cause error) (passport.OwnershipView, error) {
		return passport.OwnershipView{}, errcat.New(errcat.KeyPathOwnerUnavailable, nil, cause)
	}
	if c == nil {
		return unknown(nil)
	}
	key := reposupervisor.Key{ServerID: repo.ServerID, RepoID: repo.ID}
	c.mu.RLock()
	cache := c.results[key]
	view, hasView := c.views[repo.ServerID]
	paused := c.paused[repo.ServerID]
	epoch := c.profileEpochs[repo.ServerID]
	c.mu.RUnlock()
	r := cache.result
	if !hasView || view.RealmID != repo.RealmID || paused || cache.profileEpoch != epoch || !cache.present || cache.offline || cache.detached || cache.receivedAt.IsZero() || time.Since(cache.receivedAt) > time.Minute || r.Schema != reservationv1.AutolockSchema || r.RepoID != repo.ID || r.RepositoryState != "active" || r.PathOwnership == nil || r.Unknown || r.Stale || r.ViewGeneration < view.Generation {
		c.Schedule(repo.ServerID)
		if r.OwnershipDetail != "" {
			return unknown(errors.New(r.OwnershipDetail))
		}
		return unknown(nil)
	}
	// Matching pathnames are not proof across a new revision/replacement.
	head, err := svn.Revision(ctx, repo.RepoURL)
	if err != nil || head != r.PathOwnership.Revision {
		c.Schedule(repo.ServerID)
		return unknown(err)
	}
	info, err := svn.GetInfo(ctx, repo.LocalPath)
	if err != nil || !infoHasUUID(info, r.PathOwnership.RepositoryUUID) {
		return unknown(err)
	}
	// SVN checks may block across a reactivation or a new view. Recheck the
	// binding before returning an observation to the permission reconciler.
	c.mu.RLock()
	current := c.results[key]
	currentView := c.views[repo.ServerID]
	valid := c.profileEpochs[repo.ServerID] == epoch && !c.paused[repo.ServerID] && current.present && !current.offline && !current.detached && current.receivedAt.Equal(cache.receivedAt) && currentView.RealmID == repo.RealmID && currentView.Generation <= r.ViewGeneration && time.Since(cache.receivedAt) <= time.Minute
	c.mu.RUnlock()
	if !valid {
		return unknown(nil)
	}
	result := passport.OwnershipView{Owners: map[string]string{}, Holds: map[string]passport.OwnershipHold{}}
	for _, e := range r.PathOwnership.Entries {
		if e.Kind == "file" {
			result.Owners[e.Path] = e.OwnerRealmID
		}
	}
	for _, hold := range r.Reservations {
		result.Holds[hold.Path] = passport.OwnershipHold{Token: hold.Token, RealmID: hold.OwnerRealmID}
	}
	return result, nil
}
