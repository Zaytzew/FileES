//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"filees/pkg/commit"
	"filees/pkg/localrepo"
)

var errAnchorShellStale = errors.New("virtual folder location or preference changed")

// Desired registrations are reconstructed by prepare after a restart. This
// FIFO owns no document operation, registration authority or durable receipt.
type anchorShellQueue struct {
	mu        sync.Mutex
	active    bool
	pending   []localrepo.Record
	changed   chan struct{}
	providers map[string]*anchorShellProvider
}

// Owned until process exit AND bridge jobs/ACKs/materializations have drained.
// A repair requests EOF, never cancels or kills the connected process.
type anchorShellProvider struct {
	record    localrepo.Record
	done      chan struct{}
	mu        sync.Mutex
	stop      func()
	requested bool
}

func (p *anchorShellProvider) requestStop() {
	p.mu.Lock()
	p.requested = true
	stop := p.stop
	p.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (p *anchorShellProvider) setStop(stop func()) {
	p.mu.Lock()
	p.stop = stop
	requested := p.requested
	p.mu.Unlock()
	if requested {
		stop()
	}
}

func (p *anchorShellProvider) stopRequested() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requested
}

func shellRootKey(root string) string {
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	return root
}

func (q *anchorShellQueue) hasLocked(record localrepo.Record) bool {
	for _, r := range q.pending {
		if shellRootKey(r.LocalPath) == shellRootKey(record.LocalPath) {
			return true
		}
	}
	return false
}

func (q *anchorShellQueue) signalLocked() {
	if q.changed != nil {
		close(q.changed)
	}
	q.changed = make(chan struct{})
}

func (q *anchorShellQueue) wait(ctx context.Context, record localrepo.Record) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		if !q.hasLocked(record) {
			q.mu.Unlock()
			return nil
		}
		if q.changed == nil {
			q.changed = make(chan struct{})
		}
		changed := q.changed
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// Enqueue and start share one guard, including the gap before native connect
// announces readiness. A queued repair prevents replacement provider starts.
func (q *anchorShellQueue) beginProvider(record localrepo.Record) (*anchorShellProvider, func(), error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	key := shellRootKey(record.LocalPath)
	if q.hasLocked(record) || q.providers[key] != nil {
		return nil, nil, commit.ErrLocalStatusBusy
	}
	if q.providers == nil {
		q.providers = make(map[string]*anchorShellProvider)
	}
	p := &anchorShellProvider{record: record, done: make(chan struct{})}
	q.providers[key] = p
	var once sync.Once
	return p, func() {
		once.Do(func() {
			q.mu.Lock()
			delete(q.providers, key)
			close(p.done)
			q.signalLocked()
			q.mu.Unlock()
		})
	}, nil
}

func (q *anchorShellQueue) drainProvider(ctx context.Context, record localrepo.Record) error {
	q.mu.Lock()
	p := q.providers[shellRootKey(record.LocalPath)]
	q.mu.Unlock()
	if p == nil {
		return ctx.Err()
	}
	if !sameShellTask(p.record, record) {
		return errAnchorShellStale
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.requestStop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ctx.Err()
	}
}

func (q *anchorShellQueue) providerActive(record localrepo.Record) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.providers[shellRootKey(record.LocalPath)] != nil
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
	q.signalLocked()
	return true
}

// At most one consumer and one attempt per turn. A busy/error head stays in
// place; neither a duplicate nor a younger task may overtake it.
func (q *anchorShellQueue) runOne(ctx context.Context, apply func(context.Context, localrepo.Record) error) (bool, error) {
	return q.runFor(ctx, nil, apply)
}

func (q *anchorShellQueue) runFor(ctx context.Context, only *localrepo.Record, apply func(context.Context, localrepo.Record) error) (bool, error) {
	q.mu.Lock()
	if q.active || len(q.pending) == 0 || (only != nil && !sameShellTask(q.pending[0], *only)) {
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
		q.signalLocked()
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
	m.reconcileShellQueueFor(ctx, nil)
}

func (m *anchorManager) reconcileShellQueueFor(ctx context.Context, only *localrepo.Record) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	more, err := m.shellQueue.runFor(ctx, only, func(ctx context.Context, record localrepo.Record) error {
		if m.lifecycle == nil {
			return errAnchorShellStale
		}
		current, ok := m.lifecycle.Get(record.OperationID)
		if !ok || !sameShellTask(current, record) || !current.Anchor ||
			current.State != localrepo.StateAttached || !m.virtualEnabled(current.ServerID) {
			return errAnchorShellStale
		}
		if m.moveWaiters.Load() != 0 || (m.creationWaiters != nil && m.creationWaiters.Load() != 0) || commit.HasPendingLocalMutation(record.LocalPath) {
			return commit.ErrLocalStatusBusy
		}
		// Never retain localIntentMu while fetch/metadata ACK jobs drain.
		if err := m.shellQueue.drainProvider(ctx, record); err != nil {
			if parent.Err() == nil && !errors.Is(err, errAnchorShellStale) {
				m.log.Warnf("Explorer registration waits for provider drain at %s: %v", record.LocalPath, err)
			}
			return err
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

// Keep releases this lease only after bridge/process/cache cleanup, not when
// a stop has merely been requested. The final lifecycle check is under guard.
func (m *anchorManager) startRegisteredAnchorHelper(ctx context.Context, record localrepo.Record) (*anchorHelperStreams, func() bool, error) {
	// Preparation has finished. The FIFO may still be waiting for registration
	// or a provider lifetime to drain; that is not file-list preparation.
	m.setRuntime(record, "connecting", "")
	for {
		// A keep lifetime can help its OWN head, never run another root's
		// registration under a context which that other root does not own.
		m.reconcileShellQueueFor(ctx, &record)
		wait, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := m.shellQueue.wait(wait, record)
		cancel()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
	}
	leave, err := m.enterShellIntent(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer leave()
	if m.lifecycle != nil {
		current, ok := m.lifecycle.Get(record.OperationID)
		if !ok || !sameShellTask(current, record) || !current.Anchor || current.State != localrepo.StateAttached || !m.virtualEnabled(current.ServerID) {
			return nil, nil, errAnchorShellStale
		}
	}
	p, finished, err := m.shellQueue.beginProvider(record)
	if err != nil {
		return nil, nil, err
	}
	// Conversion or another process may own an untracked native connection.
	// Never start a competing provider or try to disconnect that process.
	info, err := m.call(ctx, "", "info", "--root", record.LocalPath)
	if err != nil || !info.OK || !info.Ours || !anchorProviderDisconnected(info.Status) {
		finished()
		if err != nil {
			return nil, nil, err
		}
		if !info.OK || !info.Ours {
			return nil, nil, fmt.Errorf("cannot verify FileES root before connect: %s %s", info.Error, info.HResult)
		}
		return nil, nil, commit.ErrLocalStatusBusy
	}
	streams, err := startAnchorHelper(ctx, m.helper, record.LocalPath)
	if err != nil {
		finished()
		return nil, nil, err
	}
	p.setStop(func() { _ = streams.stdin.Close() })
	return streams, func() bool { requested := p.stopRequested(); finished(); return requested }, nil
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
