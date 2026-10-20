//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// One provider connection, never an authority or a cache of errors. All guards,
// profile checks and revision/size binding run BEFORE consulting this cache.
// Windows keeps deny-write/delete handles on completed private materializations;
// no user document or placeholder is opened for data by this cache.
type anchorFetchCache struct {
	mu      sync.Mutex
	entries map[anchorFetchKey]*anchorFetchEntry
	closed  bool
}

type anchorFetchKey struct {
	identity       string
	revision, size int64
}

type anchorFetchEntry struct {
	done  chan struct{}
	path  string
	pin   *os.File
	err   error
	used  time.Time
	timer *time.Timer
}

const anchorFetchCacheEntries = 64
const anchorFetchCacheBytes int64 = 512 << 20
const anchorFetchCacheTTL = 15 * time.Minute // shorter than materialization cleanup (30m)

func (c *anchorFetchCache) drop(key anchorFetchKey, entry *anchorFetchEntry) {
	if c.entries[key] != entry {
		return
	}
	delete(c.entries, key)
	if entry.timer != nil {
		entry.timer.Stop()
	}
	if entry.pin != nil {
		_ = entry.pin.Close()
	}
}

func (c *anchorFetchCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for key, entry := range c.entries {
		c.drop(key, entry)
	}
}

func (c *anchorFetchCache) evictOldest(except *anchorFetchEntry) bool {
	var oldest *anchorFetchEntry
	var key anchorFetchKey
	for k, e := range c.entries {
		if e != except && e.pin != nil && (oldest == nil || e.used.Before(oldest.used)) {
			key, oldest = k, e
		}
	}
	if oldest == nil {
		return false
	}
	c.drop(key, oldest)
	return true
}

func (c *anchorFetchCache) get(ctx context.Context, key anchorFetchKey, load func() (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", errAnchorHelperGone
	}
	if c.entries == nil {
		c.entries = make(map[anchorFetchKey]*anchorFetchEntry)
	}
	if entry := c.entries[key]; entry != nil {
		entry.used = time.Now()
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-entry.done:
			return entry.path, entry.err
		}
	}
	if len(c.entries) >= anchorFetchCacheEntries && !c.evictOldest(nil) {
		c.mu.Unlock()
		return "", errors.New("too many distinct Explorer downloads in flight; retry later")
	}
	entry := &anchorFetchEntry{done: make(chan struct{}), used: time.Now()}
	c.entries[key] = entry
	c.mu.Unlock()

	entry.path, entry.err = load()
	var pin *os.File
	if entry.err == nil {
		pin, entry.err = anchorOpenMaterialization(entry.path)
	}
	if entry.err == nil {
		info, err := pin.Stat()
		if err != nil || !info.Mode().IsRegular() || (key.size >= 0 && info.Size() != key.size) {
			entry.err = errors.New("invalid Explorer materialization")
		}
	}
	c.mu.Lock()
	entry.pin = pin
	if entry.err != nil || c.closed {
		if c.closed {
			entry.err = errAnchorHelperGone
		}
		// close() may already have forgotten an in-flight entry.
		if c.entries[key] == entry {
			c.drop(key, entry)
		} else if pin != nil {
			_ = pin.Close()
		}
	} else {
		entry.timer = time.AfterFunc(anchorFetchCacheTTL, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.drop(key, entry)
		})
		// Allow one oversized file, never several retained oversized copies.
		for {
			var bytes int64
			for _, e := range c.entries {
				if e.pin != nil {
					if info, err := e.pin.Stat(); err == nil {
						bytes += info.Size()
					}
				}
			}
			if bytes <= anchorFetchCacheBytes || !c.evictOldest(entry) {
				break
			}
		}
	}
	close(entry.done)
	c.mu.Unlock()
	return entry.path, entry.err
}
