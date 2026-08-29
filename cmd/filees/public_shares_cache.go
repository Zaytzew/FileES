package main

import (
	"sort"
	"sync"

	contract "filees/pkg/contract/v1"
)

// publicShareCache aggregates the owned public-share channels discovered per
// server by refreshPublicShares as each server's projection updates. It backs
// ipcserver.PublicShareSource so a GUI can render one cross-repo panel from a
// single cached answer instead of opening repository.html per repo.
//
// A detached/removed server's entry is not actively cleared here — an
// accepted V1 simplification, matching applyRealmOwnership's own note in
// projection.go: server removal is a rare administrative action, and the
// stale entry is silently replaced (or dropped, if the server no longer owns
// any repo) the next time that server's projection happens to refresh.
type publicShareCache struct {
	mu       sync.RWMutex
	byServer map[string][]contract.PublicShareSummary
}

func newPublicShareCache() *publicShareCache {
	return &publicShareCache{byServer: make(map[string][]contract.PublicShareSummary)}
}

// Set replaces the cached shares for one server. An empty list removes the
// server's entry entirely, so a realm that stops owning any repo (or whose
// shares all got revoked/deleted) does not linger in List().
func (c *publicShareCache) Set(serverID string, shares []contract.PublicShareSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(shares) == 0 {
		delete(c.byServer, serverID)
		return
	}
	c.byServer[serverID] = shares
}

// List implements ipcserver.PublicShareSource: a flattened, deterministically
// ordered snapshot across every server currently cached.
func (c *publicShareCache) List() []contract.PublicShareSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]contract.PublicShareSummary, 0, len(c.byServer)*2)
	for _, shares := range c.byServer {
		out = append(out, shares...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServerID != out[j].ServerID {
			return out[i].ServerID < out[j].ServerID
		}
		return out[i].ChannelID < out[j].ChannelID
	})
	return out
}
