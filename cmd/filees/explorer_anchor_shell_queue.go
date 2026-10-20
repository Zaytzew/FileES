//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"filees/pkg/commit"
	"filees/pkg/localrepo"
)

var errAnchorShellStale = errors.New("virtual folder location or preference changed")

// Desired registrations are reconstructed by prepare after a restart. This
// FIFO owns no document operation, registration authority or durable receipt.
type anchorShellQueue struct {
	mu      sync.Mutex
	active  bool
	pending []localrepo.Record
}

func sameShellTask(a, b localrepo.Record) bool {
	return a.OperationID == b.OperationID && a.ServerID == b.ServerID &&
		a.RepoID == b.RepoID && a.LocalPath == b.LocalPath
}

func (q *anchorShellQueue) enqueue(record localrepo.Record) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, pending := range q.pending {
		if sameShellTask(pending, record) {
			return false // preserve its position; execution reloads preferences
		}
	}
	q.pending = append(q.pending, record)
	return true
}

// At most one consumer and one attempt per turn. A busy/error head stays in
// place; neither a duplicate nor a younger task may overtake it.
func (q *anchorShellQueue) runOne(ctx context.Context, apply func(context.Context, localrepo.Record) error) (bool, error) {
	q.mu.Lock()
	if q.active || len(q.pending) == 0 {
		q.mu.Unlock()
		return false, nil
	}
	q.active = true
	record := q.pending[0]
	q.mu.Unlock()
	err := ctx.Err()
	if err == nil {
		err = apply(ctx, record)
	}
	if err == nil {
		err = ctx.Err()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active = false
	if err == nil || errors.Is(err, errAnchorShellStale) {
		q.pending[0] = localrepo.Record{}
		q.pending = q.pending[1:]
		err = nil // obsolete locations are cancelled, never registered elsewhere
	}
	return len(q.pending) != 0, err
}

func (m *anchorManager) queueShellRegistration(record localrepo.Record) {
	if m.shellQueue.enqueue(record) {
		m.notifyLifecycle()
	}
}

func (m *anchorManager) reconcileShellQueue(ctx context.Context) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	more, err := m.shellQueue.runOne(ctx, func(ctx context.Context, record localrepo.Record) error {
		if m.lifecycle == nil {
			return errAnchorShellStale
		}
		current, ok := m.lifecycle.Get(record.OperationID)
		if !ok || !sameShellTask(current, record) || !current.Anchor ||
			current.State != localrepo.StateAttached || !m.virtualEnabled(current.ServerID) {
			return errAnchorShellStale
		}
		icon, err := anchorFolderIconPath()
		if err == nil {
			err = m.registerShellPreference(ctx, record, icon)
		}
		if err != nil && !errors.Is(err, commit.ErrLocalStatusBusy) && !errors.Is(err, errAnchorShellStale) && parent.Err() == nil {
			m.log.Warnf("Explorer registration remains queued for %s: %v", record.LocalPath, err)
		}
		return err
	})
	if more && err == nil {
		m.notifyLifecycle() // next FIFO entry; errors use the normal tick/backoff
	}
}

// Metadata observers yield briefly so a queued registration is not starved
// by many connected roots. Explicit moves/creation still take precedence.
func (m *anchorManager) enterShellIntent(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.localIntentMu == nil {
		return func() {}, nil
	}
	m.shellWaiters.Add(1)
	defer m.shellWaiters.Add(-1)
	// Longer than an already active two-second observer turn. Merely
	// yielding future turns for 500 ms could repeatedly miss its release.
	wait, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if wait.Err() != nil {
			return nil, commit.ErrLocalStatusBusy
		}
		priority := func() bool {
			return m.moveWaiters.Load() != 0 || (m.creationWaiters != nil && m.creationWaiters.Load() != 0)
		}
		if priority() {
			return nil, commit.ErrLocalStatusBusy
		}
		if m.localIntentMu.TryLock() {
			if ctx.Err() != nil || priority() {
				m.localIntentMu.Unlock()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, commit.ErrLocalStatusBusy
			}
			return m.localIntentMu.Unlock, nil
		}
		select {
		case <-wait.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, commit.ErrLocalStatusBusy
		case <-ticker.C:
		}
	}
}
