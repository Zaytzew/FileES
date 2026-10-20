//go:build !nocfapi

package main

import (
	"context"
	"time"

	"filees/pkg/commit"
)

// Only local admission is retried, never the operation or a remote send.
// Keep time for the receipted move and exact OS completion within the
// Explorer transport's 55-second deadline. Earlier caller cancellation wins.
func (m *anchorManager) enterMoveIntent(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.localIntentMu == nil {
		return func() {}, nil
	}
	m.moveWaiters.Add(1)
	defer m.moveWaiters.Add(-1)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	retry := time.NewTicker(25 * time.Millisecond)
	defer retry.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if m.localIntentMu.TryLock() {
			if err := ctx.Err(); err != nil {
				m.localIntentMu.Unlock()
				return nil, err
			}
			return m.localIntentMu.Unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, commit.ErrLocalStatusBusy
		case <-retry.C:
		}
	}
}

// Background metadata turns never queue behind user work. Recheck after
// taking the shared lifecycle mutex so a newly queued move cannot be starved
// by another observer turn. An already active turn drains normally; cancelling
// a native metadata ACK could otherwise require killing the connected helper.
func (m *anchorManager) enterObserverIntent() (func(), error) {
	if m.localIntentMu == nil {
		return func() {}, nil
	}
	if m.observerMustYield() || !m.localIntentMu.TryLock() {
		return nil, commit.ErrLocalStatusBusy
	}
	if m.observerMustYield() {
		m.localIntentMu.Unlock()
		return nil, commit.ErrLocalStatusBusy
	}
	return m.localIntentMu.Unlock, nil
}

func (m *anchorManager) observerMustYield() bool {
	return m.moveWaiters.Load() != 0 || m.shellWaiters.Load() != 0 || (m.creationWaiters != nil && m.creationWaiters.Load() != 0)
}
