package ipcserver

import "context"

// Daemon-only callback, not a request/capability or remotely supplied function.
// The manager keeps the real writer/scanner guard through provider ACK/reap and
// the final retained-copy proof. Recovery must validate its exact journal.
func (rs *RepoState) SetFileReleaseFunc(fn func(context.Context, string, bool, func() error, func() error) error) {
	rs.mu.Lock()
	rs.fileReleaseFn = fn
	rs.mu.Unlock()
}

func (rs *RepoState) WithFileRelease(ctx context.Context, rel string, recovery bool, operation func() error, complete ...func() error) error {
	rs.mu.RLock()
	fn := rs.fileReleaseFn
	rs.mu.RUnlock()
	if fn == nil {
		return ErrDepthUnavailable
	}
	if len(complete) > 1 {
		return ErrDepthUnavailable
	}
	var finish func() error
	if len(complete) == 1 {
		finish = complete[0]
	}
	return fn(ctx, rel, recovery, operation, finish)
}
