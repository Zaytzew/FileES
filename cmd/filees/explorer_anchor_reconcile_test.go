//go:build !nocfapi

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/localrepo"
)

func anchorReconcileFixture(t *testing.T) (*anchorManager, localrepo.Record) {
	t.Helper()
	root := t.TempDir()
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginAnchorAttach("test", "repo", filepath.Join(root, "point"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApproveAttach(record.OperationID, "test", "repo", "svn+ssh://example/repo", "rw"); err != nil {
		t.Fatal(err)
	}
	record, err = store.MarkAttached(record.OperationID, "repo")
	if err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{lifecycle: store, options: &anchorOptions{}, helper: filepath.Join(root, "absent-helper")}
	m.options.servers.Store(map[string]bool{"test": true})
	t.Cleanup(func() {
		m.mu.Lock()
		connections := make([]anchorConnection, 0, len(m.running))
		for _, connection := range m.running {
			connection.cancel()
			connections = append(connections, connection)
		}
		m.mu.Unlock()
		for _, connection := range connections {
			select {
			case <-connection.done:
			case <-time.After(5 * time.Second):
				t.Error("reconcile worker did not finish")
			}
		}
	})
	return m, record
}

func TestAnchorReconcileRestartsCompletedWantedConnection(t *testing.T) {
	m, record := anchorReconcileFixture(t)
	oldCtx, stop := context.WithCancel(t.Context())
	stop()
	done := make(chan struct{})
	close(done)
	key := anchorKey(record)
	m.running = map[string]anchorConnection{key: {root: record.LocalPath, ctx: oldCtx, cancel: stop, done: done}}
	m.reconcile(t.Context())
	if current := m.running[key]; current.ctx == oldCtx || current.ctx.Err() != nil || current.root != record.LocalPath {
		t.Fatal("completed connection still blocks its attached point")
	}
}

func TestAnchorReconcileWaitsForOldRootBeforeReconnect(t *testing.T) {
	for _, reason := range []string{"new-path", "already-cancelled"} {
		t.Run(reason, func(t *testing.T) {
			m, record := anchorReconcileFixture(t)
			oldCtx, stop := context.WithCancel(t.Context())
			done := make(chan struct{})
			key := anchorKey(record)
			oldRoot := record.LocalPath
			if reason == "new-path" {
				oldRoot += "-before-rename"
			} else {
				stop()
			}
			m.running = map[string]anchorConnection{key: {root: oldRoot, ctx: oldCtx, cancel: stop, done: done}}
			// Model helper drainage independently of cancellation. No second
			// provider may start until pending callbacks have finished.
			t.Cleanup(func() {
				select {
				case <-done:
				default:
					close(done)
				}
			})
			m.reconcile(t.Context())
			if oldCtx.Err() == nil {
				t.Error("provider at the old location was not stopped")
			}
			if m.running[key].ctx != oldCtx {
				t.Fatal("replacement overlapped the retiring provider")
			}
			close(done)
			m.reconcile(t.Context())
			current := m.running[key]
			if current.ctx == oldCtx || current.ctx.Err() != nil || current.root != record.LocalPath {
				t.Fatal("retired connection was not replaced at the current location")
			}
		})
	}
}

func TestAnchorReconcileKeepsLiveUnchangedConnection(t *testing.T) {
	m, record := anchorReconcileFixture(t)
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	key := anchorKey(record)
	m.running = map[string]anchorConnection{key: {root: record.LocalPath, ctx: ctx, cancel: stop, done: done}}
	t.Cleanup(func() { close(done) })
	m.reconcile(t.Context())
	if m.running[key].ctx != ctx || ctx.Err() != nil {
		t.Fatal("unchanged live provider was replaced")
	}
}

func TestAnchorReconcileRemovesCompletedDisabledConnection(t *testing.T) {
	m, record := anchorReconcileFixture(t)
	ctx, stop := context.WithCancel(t.Context())
	stop()
	done := make(chan struct{})
	close(done)
	key := anchorKey(record)
	m.running = map[string]anchorConnection{key: {root: record.LocalPath, ctx: ctx, cancel: stop, done: done}}
	m.options.servers.Store(map[string]bool{})
	m.reconcile(t.Context())
	if _, exists := m.running[key]; exists {
		t.Fatal("disabled point was reconnected")
	}
}
