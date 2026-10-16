package main

import (
	"context"
	"errors"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/publicstate"
	"filees/pkg/reservationclient"
	"filees/pkg/talk"
)

// Only the per-server refresh coordinator accesses capability backoff. The
// protocol snapshot itself lives in the synchronized cache alongside own ACKs.
type publicLinksMirror struct {
	epoch            uint64
	unsupportedUntil time.Time
}

type publicLinksStateFetcher interface {
	FetchPublicLinks(context.Context, string, publicstate.Request) (publicstate.Response, error)
}

// true means the modern lane handled this attempt (including an error).
// Only an explicit unsupported schema may select the legacy fallback.
func refreshPublicLinksState(ctx context.Context, lister publicShareLister, cache publicShareCacheSetter, serverID string, view clientview.View, epoch uint64, mirror *publicLinksMirror) bool {
	reader, ok := lister.(publicLinksStateFetcher)
	stateCache, hasStateCache := cache.(*publicShareCache)
	if !ok || mirror == nil || !hasStateCache {
		return false
	}
	if mirror.epoch != epoch {
		*mirror = publicLinksMirror{epoch: epoch}
	}
	if time.Now().Before(mirror.unsupportedUntil) {
		return false
	}
	req, serial := stateCache.stateRequest(serverID, view.RealmID, epoch)
	result, err := reader.FetchPublicLinks(ctx, serverID, req)
	if errors.Is(err, reservationclient.ErrPublicLinksUnsupported) {
		mirror.unsupportedUntil = time.Now().Add(5 * time.Minute) // capability only, not data validity
		return false
	}
	if ctx.Err() == context.Canceled {
		return true
	}
	if err == nil {
		binding := result.Binding // transport checked the activated client ID
		binding.ServerID, binding.RealmID = serverID, view.RealmID
		err = stateCache.completeState(serverID, epoch, serial, req, result, binding)
	}
	if errors.Is(err, errPublicLinksSuperseded) {
		return true
	}
	if err != nil {
		// Mark the whole attempt unverified without dropping old rows. Never
		// turn transport/parse/authorization failure into an empty inventory.
		if failed, ok := cache.(interface{ FailRefresh(string, uint64) }); ok {
			failed.FailRefresh(serverID, epoch)
		}
		talk.With("public-shares:"+serverID).Warnf("differential listing remains unverified: %v", err)
		return true
	}
	return true
}
