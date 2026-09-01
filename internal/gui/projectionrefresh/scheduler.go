// Package projectionrefresh coordinates the desktop client's state-emission
// lane. It deliberately knows nothing about the remote emission envelope:
// transport and projection decoding remain the responsibility of the caller.
package projectionrefresh

import (
	"context"
	"strings"
	"sync"
	"time"
)

// runDeadline bounds one refresh callback. It is a backstop, not a transport
// timeout: the transport owns its own, shorter limit. Without it a callback
// that never returns pins its server's entry in the map forever, and because
// Schedule only raises pending for an entry that is already present, that
// server's lane never runs again until the daemon restarts.
const runDeadline = 3 * time.Minute

// RefreshFunc performs one state refresh for serverID. The function owns the
// result, including publishing a successful emission or recording a failed
// attempt. It must observe ctx so Scheduler.Close can stop promptly.
type RefreshFunc func(ctx context.Context, serverID string)

// Scheduler keeps at most one state refresh in flight per server. Triggers
// received while that refresh runs are coalesced into one subsequent pass.
// Different servers are intentionally independent and may refresh in parallel.
//
// The transactional lane is not represented here. Keeping this coordinator
// separate from mutation execution is what allows a state request to proceed
// while a long-running transaction uses its own SSH connection.
type Scheduler struct {
	ctx     context.Context
	cancel  context.CancelFunc
	refresh RefreshFunc

	// runTimeout bounds a single callback; tests shorten it.
	runTimeout time.Duration

	mu      sync.Mutex
	closed  bool
	servers map[string]*serverState
	workers sync.WaitGroup
}

type serverState struct {
	pending bool
}

// New creates a scheduler bound to parent. A nil refresh function produces a
// disabled scheduler whose Schedule method consistently returns false.
func New(parent context.Context, refresh RefreshFunc) *Scheduler {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Scheduler{
		ctx:        ctx,
		cancel:     cancel,
		refresh:    refresh,
		runTimeout: runDeadline,
		servers:    make(map[string]*serverState),
	}
}

// Schedule requests a state refresh for one canonical server ID. It returns
// false when the request is invalid or the scheduler is already stopping.
func (s *Scheduler) Schedule(serverID string) bool {
	if s == nil || s.refresh == nil || serverID == "" || strings.TrimSpace(serverID) != serverID {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return false
	}
	if state, running := s.servers[serverID]; running {
		state.pending = true
		return true
	}

	state := &serverState{}
	s.servers[serverID] = state
	s.workers.Add(1)
	go s.run(serverID, state)
	return true
}

func (s *Scheduler) run(serverID string, state *serverState) {
	defer s.workers.Done()
	for {
		if s.ctx.Err() == nil {
			s.runOnce(serverID)
		}

		s.mu.Lock()
		if s.closed || s.ctx.Err() != nil || !state.pending {
			if s.servers[serverID] == state {
				delete(s.servers, serverID)
			}
			s.mu.Unlock()
			return
		}
		state.pending = false
		s.mu.Unlock()
	}
}

// runOnce invokes the callback under its own deadline so a stuck refresh
// releases the server's slot instead of disabling the lane permanently.
func (s *Scheduler) runOnce(serverID string) {
	timeout := s.runTimeout
	if timeout <= 0 {
		timeout = runDeadline
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	s.refresh(ctx, serverID)
}

// Close rejects new triggers, cancels in-flight callbacks, and waits for all
// state-lane workers to finish. It is safe to call more than once.
func (s *Scheduler) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	s.workers.Wait()
}
