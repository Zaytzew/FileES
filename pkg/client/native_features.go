package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Only successful, validated capability receipts are cached, never authority,
// WC state, credentials or mutation results. The bounded process-local cache
// coalesces probes across clients. Stat identity is checked before and after
// probing and on every reuse; a replaced/changed image must be probed again.
type nativeFeatureEntry struct {
	info     os.FileInfo
	ready    chan struct{}
	features map[string]bool
}

type nativeFeatureCache struct {
	mu      sync.Mutex
	entries map[string]*nativeFeatureEntry
}

var nativeFeatures = &nativeFeatureCache{}

func sameNativeImage(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

func (cache *nativeFeatureCache) get(ctx context.Context, executable string, probe func(context.Context) (map[string]any, error)) (map[string]bool, error) {
	if !filepath.IsAbs(executable) {
		return nil, fmt.Errorf("native SVN capability probe requires absolute executable")
	}
	executable = filepath.Clean(executable)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(executable)
		if err != nil {
			return nil, fmt.Errorf("inspect native SVN image: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("native SVN image is not a regular file")
		}
		cache.mu.Lock()
		if entry := cache.entries[executable]; entry != nil {
			if entry.ready != nil {
				ready := entry.ready
				cache.mu.Unlock()
				select {
				case <-ready:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if sameNativeImage(info, entry.info) {
				features := entry.features
				cache.mu.Unlock()
				return features, nil
			}
			delete(cache.entries, executable)
		}
		if cache.entries == nil {
			cache.entries = make(map[string]*nativeFeatureEntry)
		}
		// Evict only completed receipts: in-flight waiters must retain their gate.
		if len(cache.entries) >= 64 {
			for key, entry := range cache.entries {
				if entry.ready == nil {
					delete(cache.entries, key)
					break
				}
			}
		}
		if len(cache.entries) >= 64 {
			var ready chan struct{}
			for _, entry := range cache.entries {
				ready = entry.ready
				break
			}
			cache.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		entry := &nativeFeatureEntry{info: info, ready: make(chan struct{})}
		cache.entries[executable] = entry
		cache.mu.Unlock()
		raw, err := probe(ctx)
		features := make(map[string]bool)
		if err == nil {
			list, ok := raw["features"].([]any)
			if raw["schema"] != nativeSchema || raw["ok"] != true || !ok {
				err = fmt.Errorf("native SVN invalid capability receipt")
			} else {
				for _, v := range list {
					s, ok := v.(string)
					if !ok || s == "" {
						err = fmt.Errorf("native SVN invalid capability name")
						break
					}
					features[s] = true
				}
			}
		}
		if err == nil {
			after, e := os.Stat(executable)
			if e != nil || !sameNativeImage(info, after) {
				err = fmt.Errorf("native SVN image changed during capability probe")
			}
		}
		cache.mu.Lock()
		ready := entry.ready
		entry.ready = nil
		if err != nil {
			delete(cache.entries, executable)
		} else {
			entry.features = features
		}
		close(ready)
		cache.mu.Unlock()
		return features, err
	}
}
