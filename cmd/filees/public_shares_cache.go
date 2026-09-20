package main

import (
	"sort"
	"sync"
	"time"

	contract "filees/pkg/contract/v1"
)

// publicShareCache aggregates the owned public-share channels discovered per
// server by refreshPublicShares as each server's projection updates. It backs
// ipcserver.PublicShareSource so a GUI can render one cross-repo panel from a
// single cached answer instead of opening repository.html per repo.
type publicShareCache struct {
	mu         sync.RWMutex
	byServer   map[string][]contract.PublicShareSummary
	observed   map[string]time.Time
	failed     map[string]bool
	blocked    map[string]bool
	generation uint64
	now        func() time.Time
}

func newPublicShareCache() *publicShareCache {
	return &publicShareCache{byServer: make(map[string][]contract.PublicShareSummary), observed: map[string]time.Time{}, failed: map[string]bool{}, blocked: map[string]bool{}, now: time.Now}
}

// Set replaces the cached shares for one server, retaining the observation
// time even when the complete result is empty.
func (c *publicShareCache) Set(serverID string, shares []contract.PublicShareSummary) {
	c.SetPartial(serverID, shares, nil)
}

// Retain the last known rows of failed repositories; a partial refresh must
// not make their links disappear or claim the whole aggregate is current.
func (c *publicShareCache) SetPartial(serverID string, shares []contract.PublicShareSummary, failed []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocked[serverID] {
		return
	}
	now := c.now().UTC()
	rows := append([]contract.PublicShareSummary(nil), shares...)
	for i := range rows {
		rows[i].ObservedAt = now.Format(time.RFC3339Nano)
	}
	missing := map[string]bool{}
	for _, repoID := range failed {
		missing[repoID] = true
	}
	for _, old := range c.byServer[serverID] {
		if missing[old.RepoID] {
			rows = append(rows, old)
		}
	}
	c.byServer[serverID] = rows
	c.failed[serverID] = len(failed) > 0
	if len(failed) == 0 {
		c.observed[serverID] = now
	}
	c.generation++
}

// Track makes an unread server explicit; zero shares before the first
// successful refresh are not a verified empty aggregate.
func (c *publicShareCache) Track(serverID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.byServer[serverID]; !exists && !c.blocked[serverID] {
		c.byServer[serverID] = nil
		c.generation++
	}
}

func (c *publicShareCache) SetDetached(serverID string, detached bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked[serverID] = detached
	if detached {
		delete(c.byServer, serverID)
		delete(c.observed, serverID)
		delete(c.failed, serverID)
		c.generation++
	}
}

func (c *publicShareCache) Snapshot() contract.PublicShareListResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := contract.PublicShareListResult{Shares: []contract.PublicShareSummary{}, Generation: c.generation}
	var oldest time.Time
	unknown := false
	now := c.now()
	for serverID, shares := range c.byServer {
		observed := c.observed[serverID]
		unknown = unknown || observed.IsZero()
		stale := c.failed[serverID] || observed.IsZero() || now.Sub(observed) > 5*time.Minute
		result.Stale = result.Stale || stale
		if oldest.IsZero() || observed.Before(oldest) {
			oldest = observed
		}
		for _, share := range shares {
			share.Stale = stale
			result.Shares = append(result.Shares, share)
		}
	}
	if result.Stale {
		for i := range result.Shares {
			result.Shares[i].Stale = true
		}
	}
	if !unknown && !oldest.IsZero() {
		result.AsOf = oldest.UTC().Format(time.RFC3339Nano)
	}
	sort.Slice(result.Shares, func(i, j int) bool {
		a, b := result.Shares[i], result.Shares[j]
		if a.ServerID != b.ServerID {
			return a.ServerID < b.ServerID
		}
		return a.ChannelID < b.ChannelID
	})
	return result
}

// List implements ipcserver.PublicShareSource: a flattened, deterministically
// ordered snapshot across every server currently cached.
func (c *publicShareCache) List() []contract.PublicShareSummary { return c.Snapshot().Shares }
