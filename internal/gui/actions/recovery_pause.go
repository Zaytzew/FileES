package actions

import (
	"context"
	"sync"
	"sync/atomic"
)

// countedTasks retains WaitGroup semantics and exposes an idle check only to
// the dispatch loop. Recovery must not use operation keys as task lifetimes.
type countedTasks struct {
	sync.WaitGroup
	count atomic.Int64
}

func (tasks *countedTasks) Add(n int) {
	tasks.count.Add(int64(n))
	tasks.WaitGroup.Add(n)
}

func (tasks *countedTasks) Done() {
	tasks.WaitGroup.Done()
	tasks.count.Add(-1)
}

// PauseIfIdle permanently pauses dispatch for a GUI-only restart, without
// cancelling or replaying work. The caller must stop external admission while
// calling this method and keep it stopped on success. Busy means try later.
func (c *Controller) PauseIfIdle(ctx context.Context) bool {
	reply := make(chan bool, 1)
	select {
	case c.pauseRequests <- reply:
		// Once accepted, wait for the decision even if ctx is cancelled: a
		// positive decision must never be lost with admission reopened.
		return <-reply
	case <-ctx.Done():
		return false
	}
}
