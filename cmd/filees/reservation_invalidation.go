package main

import (
	"filees/pkg/reposupervisor"
	"path/filepath"
	"sync"
)

// A terminal token ACK removes only that observed token from presentation.
// It never confirms that the path is currently unheld or grants write access.
func (c *reservationProjectionCoordinator) ObserveRelease(key reposupervisor.Key, wc, path, token string) {
	if c == nil {
		return
	}
	rel, err := filepath.Rel(wc, path)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.results[key]
	rows := r.result.Reservations[:0:0]
	for _, row := range r.result.Reservations {
		if row.Path != rel || row.Token != token {
			rows = append(rows, row)
		}
	}
	r.result.Reservations = rows
	c.results[key] = r // observedAt and invalidation intentionally unchanged
}

// The generation fences in-flight reads. Invalidated payloads remain visible
// and usable as differential baselines, never as fresh permission evidence.
func (c *reservationProjectionCoordinator) invalidateLocked(key reposupervisor.Key) {
	if c.mutationEpochs == nil {
		c.mutationEpochs = make(map[reposupervisor.Key]uint64)
	}
	c.mutationEpochs[key]++
	r := c.results[key]
	r.invalidated = true
	c.results[key] = r
}

// BeginMutation is cheap and local. Nested mutations coalesce their final
// observation. Failure still invalidates: a lost response is not a rollback.
func (c *reservationProjectionCoordinator) BeginMutation(key reposupervisor.Key) func() {
	if c == nil {
		return func() {}
	}
	c.mu.Lock()
	if c.mutating == nil {
		c.mutating = make(map[reposupervisor.Key]int)
	}
	c.mutating[key]++
	c.invalidateLocked(key)
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.mutating[key]--
			c.invalidateLocked(key)
			done := c.mutating[key] == 0
			c.mu.Unlock()
			if done && c.scheduler != nil && !c.Paused(key.ServerID) {
				c.scheduler.Schedule(key.ServerID)
			}
		})
	}
}
