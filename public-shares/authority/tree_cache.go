package authority

import "sync"

// TreeCache bounds immutable SVN tree enumerations.  The key includes the
// numeric revision, so a following channel cannot observe stale HEAD state.
// Policy and channel records are intentionally not cached here.
type TreeCache struct {
	mu      sync.Mutex
	limit   int
	entries map[treeCacheKey][]TreeObject
	order   []treeCacheKey
}

type treeCacheKey struct {
	repoID, sourceRoot string
	revision           int64
}

func NewTreeCache(limit int) *TreeCache {
	if limit < 1 {
		limit = 1
	}
	return &TreeCache{limit: limit, entries: make(map[treeCacheKey][]TreeObject)}
}

func (c *TreeCache) get(key treeCacheKey) ([]TreeObject, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	objects, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.touch(key)
	return append([]TreeObject(nil), objects...), true
}

func (c *TreeCache) put(key treeCacheKey, objects []TreeObject) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		c.entries[key] = append([]TreeObject(nil), objects...)
		c.touch(key)
		return
	}
	for len(c.entries) >= c.limit && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[key] = append([]TreeObject(nil), objects...)
	c.order = append(c.order, key)
}

func (c *TreeCache) touch(key treeCacheKey) {
	for index, candidate := range c.order {
		if candidate != key {
			continue
		}
		copy(c.order[index:], c.order[index+1:])
		c.order[len(c.order)-1] = key
		return
	}
}
