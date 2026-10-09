package ipcserver

import (
	"context"
	"filees/pkg/watcher"
)

// Daemon-only writer/scanner entry, never supplied as executable IPC content.
func (rs *RepoState) SetTreeMutationFunc(fn func(context.Context, string, string, *watcher.TreeMoveSnapshot, func(*watcher.TreeMoveSnapshot) error, func() error) error) {
	rs.mu.Lock()
	rs.treeMutationFn = fn
	rs.mu.Unlock()
}

func (rs *RepoState) WithTreeMutation(ctx context.Context, source, destination string, recovery *watcher.TreeMoveSnapshot, operation func(*watcher.TreeMoveSnapshot) error, complete func() error) error {
	rs.mu.RLock()
	fn := rs.treeMutationFn
	rs.mu.RUnlock()
	if fn == nil {
		return ErrDepthUnavailable
	}
	return fn(ctx, source, destination, recovery, operation, complete)
}
