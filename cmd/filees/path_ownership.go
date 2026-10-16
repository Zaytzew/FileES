package main

import (
	"context"
	"fmt"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientview"
	"filees/pkg/config"
	"filees/pkg/errcat"
	"filees/pkg/passport"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
)

// Ownership reads only the existing broker lane's latest observation. It never
// opens another broker connection or revives an on-disk stale projection.
func (c *reservationProjectionCoordinator) Ownership(ctx context.Context, repo config.Repo, svn client.Client) (passport.OwnershipView, error) {
	unknown := func(reason string, cause error) (passport.OwnershipView, error) {
		if cause != nil {
			cause = fmt.Errorf("ownership %s: %w", reason, cause)
		} else {
			cause = fmt.Errorf("ownership %s", reason)
		}
		return passport.OwnershipView{}, errcat.New(errcat.KeyPathOwnerUnavailable, nil, cause)
	}
	if c == nil {
		return unknown("coordinator_unavailable", nil)
	}
	key := reposupervisor.Key{ServerID: repo.ServerID, RepoID: repo.ID}
	c.mu.RLock()
	cache := c.results[key]
	view, hasView := c.views[repo.ServerID]
	paused := c.paused[repo.ServerID]
	epoch := c.profileEpochs[repo.ServerID]
	c.mu.RUnlock()
	r := cache.result
	if reason := ownershipCacheReason(repo, cache, view, hasView, paused, epoch, time.Now()); reason != "" {
		c.Poll(repo.ServerID)
		if r.OwnershipDetail != "" {
			reason += "; server_detail=" + r.OwnershipDetail
		}
		return unknown(reason, nil)
	}
	// Matching pathnames are not proof across a new revision/replacement.
	head, err := svn.Revision(ctx, repo.RepoURL)
	if err != nil {
		c.Poll(repo.ServerID)
		return unknown("head_read_failed", err)
	}
	if head != r.PathOwnership.Revision {
		c.Schedule(repo.ServerID)
		return unknown(fmt.Sprintf("revision_mismatch observed=%d head=%d", r.PathOwnership.Revision, head), nil)
	}
	info, err := svn.GetInfo(ctx, repo.LocalPath)
	if err != nil {
		return unknown("working_copy_info_failed", err)
	}
	if !infoHasUUID(info, r.PathOwnership.RepositoryUUID) {
		return unknown("working_copy_incarnation_mismatch", nil)
	}
	// SVN checks may block across a reactivation or a new view. Recheck the
	// binding before returning an observation to the permission reconciler.
	c.mu.RLock()
	current := c.results[key]
	currentView, hasCurrentView := c.views[repo.ServerID]
	reason := ownershipCacheReason(repo, current, currentView, hasCurrentView, c.paused[repo.ServerID], c.profileEpochs[repo.ServerID], time.Now())
	if c.profileEpochs[repo.ServerID] != epoch {
		reason = "activation_changed"
	} else if reason == "" && !current.receivedAt.Equal(cache.receivedAt) {
		reason = "observation_changed"
	} else if reason == "" && currentView.Generation > r.ViewGeneration {
		reason = "view_changed"
	}
	c.mu.RUnlock()
	if reason != "" {
		return unknown("after_svn_check: "+reason, nil)
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

// Return a stable diagnostic discriminator without paths, tokens or credentials.
// The same gates run again after blocking SVN reads; diagnostics never authorize.
func ownershipCacheReason(repo config.Repo, cache cachedReservationResult, view clientview.View, hasView, paused bool, epoch uint64, now time.Time) string {
	r := cache.result
	switch {
	case !hasView:
		return "view_missing"
	case view.RealmID != repo.RealmID:
		return "realm_mismatch"
	case paused:
		return "state_lane_paused"
	case cache.profileEpoch != epoch:
		return "activation_mismatch"
	case !cache.present:
		return "observation_missing"
	case cache.offline:
		return "state_lane_offline"
	case cache.detached:
		return "client_detached"
	case cache.receivedAt.IsZero():
		return "receipt_missing"
	case now.Before(cache.receivedAt):
		return "receipt_in_future"
	case now.Sub(cache.receivedAt) > time.Minute:
		return fmt.Sprintf("observation_expired age_ms=%d limit_ms=60000", now.Sub(cache.receivedAt).Milliseconds())
	case r.Schema != reservationv1.AutolockSchema:
		return "ownership_schema_unavailable"
	case r.RepoID != repo.ID:
		return "repository_mismatch"
	case r.RepositoryState != "active":
		return "repository_not_active"
	case r.Unknown:
		return "server_state_unknown"
	case r.Stale:
		return "server_state_stale"
	case r.PathOwnership == nil:
		return "ownership_snapshot_missing"
	case r.ViewGeneration < view.Generation:
		return fmt.Sprintf("view_generation_behind observed=%d required=%d", r.ViewGeneration, view.Generation)
	default:
		return ""
	}
}
