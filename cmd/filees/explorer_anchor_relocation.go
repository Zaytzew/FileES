//go:build !nocfapi

package main

import (
	"context"
	"errors"

	"filees/pkg/localrepo"
)

// quiesceRelocation only disconnects the existing provider. It must not
// unregister the point: that could discard remote-only placeholders.
// The durable relocating fence prevents reconcile from starting it again.
func (m *anchorManager) quiesceRelocation(ctx context.Context, record localrepo.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.lifecycle == nil {
		return errors.New("anchor lifecycle is unavailable")
	}
	current, ok := m.lifecycle.Get(record.OperationID)
	if !ok || !current.Anchor || current.State != localrepo.StateRelocating ||
		current.ServerID != record.ServerID || current.RepoID != record.RepoID ||
		current.LocalPath != record.LocalPath || current.PendingLocalPath != record.PendingLocalPath {
		return errors.New("anchor relocation fence changed before provider drain")
	}
	m.mu.Lock()
	connection, active := m.running[anchorKey(record)]
	if active && connection.root != record.LocalPath {
		m.mu.Unlock()
		return errors.New("anchor provider root differs from relocation source")
	}
	if active {
		connection.cancel()
	}
	m.mu.Unlock()
	if active {
		select {
		case <-connection.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	after, ok := m.lifecycle.Get(record.OperationID)
	if !ok || after.State != localrepo.StateRelocating || after.RemoteDeletionObserved ||
		after.LocalPath != current.LocalPath || after.PendingLocalPath != current.PendingLocalPath ||
		after.RepoURL != current.RepoURL || after.Access != current.Access {
		return errors.New("anchor relocation fence changed during provider drain")
	}
	return ctx.Err()
}

func (m *anchorManager) notifyLifecycle() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
