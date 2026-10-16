package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"filees/internal/durable"
	contract "filees/pkg/contract/v1"
	"filees/pkg/privatefile"
	"filees/pkg/publicstate"
	"filees/pkg/talk"
)

// publicShareCache aggregates the owned public-share channels discovered per
// server by refreshPublicShares as each server's projection updates. It backs
// ipcserver.PublicShareSource so a GUI can render one cross-repo panel from a
// single cached answer instead of opening repository.html per repo.
type publicShareCache struct {
	mu                sync.RWMutex
	byServer          map[string][]contract.PublicShareSummary
	observed          map[string]time.Time
	failed            map[string]bool
	blocked           map[string]bool
	loading           map[string]bool
	bindings          map[string]publicShareCacheBinding
	epochs            map[string]uint64
	generation        uint64
	now               func() time.Time
	states            map[string]publicstate.Snapshot
	stateSerial       map[string]uint64
	refreshSerial     map[string]uint64
	mutating          map[string]bool
	mutationConfirmed map[string]bool
	mutationGates     map[string]chan struct{}
}

func newPublicShareCache() *publicShareCache {
	return &publicShareCache{byServer: make(map[string][]contract.PublicShareSummary), observed: map[string]time.Time{}, failed: map[string]bool{}, blocked: map[string]bool{}, loading: map[string]bool{}, bindings: map[string]publicShareCacheBinding{}, epochs: map[string]uint64{}, now: time.Now, states: map[string]publicstate.Snapshot{}, stateSerial: map[string]uint64{}, mutating: map[string]bool{}, mutationGates: map[string]chan struct{}{}}
}

// The disposable presenter cache is scoped to one activation, not just a
// user-chosen server label. It cannot authorize actions or prove freshness.
type publicShareCacheBinding struct {
	ServerID string `json:"server_id"`
	ClientID string `json:"client_id"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Path     string `json:"-"`
}

type publicShareCacheFile struct {
	Schema   int                           `json:"schema"`
	Binding  publicShareCacheBinding       `json:"binding"`
	Observed time.Time                     `json:"observed"`
	Shares   []contract.PublicShareSummary `json:"shares"`
}

const publicShareCacheLimit = 32 << 20

func (c *publicShareCache) Bind(serverID, clientID, address string, port int, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	binding := publicShareCacheBinding{serverID, clientID, address, port, path}
	if c.bindings[serverID] == binding {
		return
	}
	c.epochs[serverID]++
	c.bindings[serverID] = binding
	delete(c.states, serverID)
	delete(c.byServer, serverID)
	delete(c.observed, serverID)
	delete(c.failed, serverID)
	delete(c.loading, serverID)
	c.generation++
	if !filepath.IsAbs(path) || clientID == "" || c.blocked[serverID] {
		return
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return
	}
	if err == nil {
		defer file.Close()
		err = privatefile.Verify(path)
	}
	var saved publicShareCacheFile
	if err == nil {
		raw, readErr := io.ReadAll(io.LimitReader(file, publicShareCacheLimit+1))
		err = readErr
		if err == nil && len(raw) > publicShareCacheLimit {
			err = errors.New("cache exceeds size limit")
		}
		if err == nil {
			err = json.Unmarshal(raw, &saved)
		}
	}
	if err != nil {
		talk.With("public-shares:"+serverID).Warnf("ignore disposable local listing cache: %v", err)
		return
	}
	wanted := binding
	wanted.Path = ""
	if saved.Schema != 1 || saved.Binding != wanted {
		return
	}
	for i := range saved.Shares {
		if saved.Shares[i].ServerID != serverID {
			return
		}
		saved.Shares[i].Stale = true
	}
	c.byServer[serverID] = saved.Shares
	c.observed[serverID] = saved.Observed
	c.failed[serverID] = true // even a recent saved observation needs a live check
}

func (c *publicShareCache) BeginRefresh(serverID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshSerial == nil {
		c.refreshSerial = make(map[string]uint64)
	}
	c.refreshSerial[serverID] = c.stateSerial[serverID]
	if !c.blocked[serverID] {
		if _, exists := c.byServer[serverID]; !exists {
			c.byServer[serverID] = nil
		}
		c.loading[serverID] = true
		c.generation++
	}
	return c.epochs[serverID]
}

func (c *publicShareCache) EndRefresh(serverID string, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs[serverID] == epoch && c.loading[serverID] {
		delete(c.loading, serverID)
		c.generation++
	}
}

func (c *publicShareCache) CompleteRefresh(serverID string, epoch uint64, shares []contract.PublicShareSummary, failed []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs[serverID] == epoch && c.refreshSerial[serverID] == c.stateSerial[serverID] && !c.mutating[serverID] {
		c.setPartialLocked(serverID, shares, failed)
	}
}

// A failed aggregate verification is not evidence that any link disappeared.
func (c *publicShareCache) FailRefresh(serverID string, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs[serverID] == epoch && !c.blocked[serverID] {
		c.failed[serverID] = true
		c.generation++
	}
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
	c.setPartialLocked(serverID, shares, failed)
}

func (c *publicShareCache) setPartialLocked(serverID string, shares []contract.PublicShareSummary, failed []string) {
	if c.blocked[serverID] {
		return
	}
	now := c.now().UTC()
	rows := append([]contract.PublicShareSummary(nil), shares...)
	for i := range rows {
		rows[i].ObservedAt = now.Format(time.RFC3339Nano)
		rows[i].Stale = false
	}
	missing := map[string]bool{}
	for _, repoID := range failed {
		missing[repoID] = true
	}
	for _, old := range c.byServer[serverID] {
		if missing[old.RepoID] {
			old.Stale = true
			rows = append(rows, old)
		}
	}
	c.byServer[serverID] = rows
	c.failed[serverID] = len(failed) > 0
	if len(failed) == 0 {
		c.observed[serverID] = now
	}
	c.generation++
	c.saveLocked(serverID)
}

func (c *publicShareCache) saveLocked(serverID string) {
	binding := c.bindings[serverID]
	if !filepath.IsAbs(binding.Path) || binding.ClientID == "" {
		return
	}
	err := writePublicShareCache(binding.Path, publicShareCacheFile{1, binding, c.observed[serverID], c.byServer[serverID]})
	if err != nil {
		talk.With("public-shares:"+serverID).Warnf("cannot save disposable local listing cache: %v", err)
	}
}

func writePublicShareCache(path string, saved publicShareCacheFile) error {
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if len(raw) > publicShareCacheLimit {
		return errors.New("cache exceeds size limit")
	}
	// The parent is the existing private activation directory. Never recreate
	// it after activation removal; cache failure must not block live listing.
	if err := privatefile.Verify(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".public-shares-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := privatefile.Harden(tmp.Name()); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(path))
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
		c.epochs[serverID]++
		delete(c.loading, serverID)
		if path := c.bindings[serverID].Path; path != "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				talk.With("public-shares:"+serverID).Warnf("cannot remove detached listing cache: %v", err)
			}
		}
		delete(c.bindings, serverID)
		delete(c.states, serverID)
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
		result.Loading = result.Loading || c.loading[serverID]
		observed := c.observed[serverID]
		unknown = unknown || observed.IsZero()
		stale := c.failed[serverID] || observed.IsZero() || now.Sub(observed) > 5*time.Minute
		result.Stale = result.Stale || stale
		if oldest.IsZero() || observed.Before(oldest) {
			oldest = observed
		}
		for _, share := range shares {
			// Aggregate completeness and row freshness are independent: an
			// unread server or failed sibling repo cannot invalidate this row.
			rowObserved, err := time.Parse(time.RFC3339Nano, share.ObservedAt)
			share.Stale = share.Stale || err != nil || now.Sub(rowObserved) > 5*time.Minute
			result.Shares = append(result.Shares, share)
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
