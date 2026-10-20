//go:build windows && !nocfapi

package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"filees/pkg/localrepo"
)

func TestShellQueueStaleTaskDoesNotStopOldProvider(t *testing.T) {
	m, r := shellQueueUnitManager(t)
	p, finish, err := m.shellQueue.beginProvider(r)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	m.queueShellRegistration(r)
	if _, err = m.lifecycle.BeginRelocation("fixture", "repo", filepath.Join(t.TempDir(), "moved"), true); err != nil {
		t.Fatal(err)
	}
	m.reconcileShellQueue(t.Context())
	if p.stopRequested() || len(m.shellQueue.pending) != 0 {
		t.Fatal("obsolete repair touched provider or blocked queue")
	}
}

func TestShellQueueDrainLeavesIntentGuardAvailableToACK(t *testing.T) {
	m, r := shellQueueUnitManager(t)
	p, finish, err := m.shellQueue.beginProvider(r)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	m.queueShellRegistration(r)
	ack := make(chan bool, 1)
	p.setStop(func() {
		go func() {
			available := m.localIntentMu.TryLock()
			if available {
				m.localIntentMu.Unlock()
			}
			ack <- available
			finish()
		}()
	})
	m.reconcileShellQueue(t.Context()) // missing helper then retains retry, not a fake success
	if !<-ack {
		t.Fatal("drain retained lifecycle guard needed by ACK")
	}
	if len(m.shellQueue.pending) != 1 {
		t.Fatal("helper failure discarded retry")
	}
}

func shellQueueUnitManager(t *testing.T) (*anchorManager, localrepo.Record) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.BeginAnchorAttach("fixture", "repo", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(r.OperationID, "fixture", "repo", "svn+ssh://example/repo", "rw"); err != nil {
		t.Fatal(err)
	}
	r, err = store.MarkAttached(r.OperationID, "repo")
	if err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: filepath.Join(root, "absent-helper.exe"), lifecycle: store, localIntentMu: new(sync.Mutex), options: &anchorOptions{}}
	m.options.servers.Store(map[string]bool{"fixture": true})
	return m, r
}

func TestShellQueueRechecksLocationAfterWaitingForAdmission(t *testing.T) {
	m, r := shellQueueUnitManager(t)
	m.queueShellRegistration(r)
	m.localIntentMu.Lock()
	done := make(chan struct{})
	go func() { defer close(done); m.reconcileShellQueue(t.Context()) }()
	deadline := time.Now().Add(2 * time.Second)
	for m.shellWaiters.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.shellWaiters.Load() == 0 {
		m.localIntentMu.Unlock()
		<-done
		t.Fatal("worker did not request admission")
	}
	_, err := m.lifecycle.BeginRelocation("fixture", "repo", filepath.Join(t.TempDir(), "moved"), true)
	m.localIntentMu.Unlock()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	// If the post-lock authority check were absent, the missing helper would
	// fail and retain the head. No Windows registration verb may run here.
	if len(m.shellQueue.pending) != 0 {
		t.Fatal("stale root reached helper after wait")
	}
}

func TestShellQueueSavedVisibilityRetriesWithoutAnotherUserAction(t *testing.T) {
	m, r := shellQueueUnitManager(t)
	m.queueShellRegistration(r)
	yes := true
	_, err := m.ExplorerShortcut(context.Background(), "fixture", "repo", &yes, r.OperationID)
	if err == nil {
		t.Fatal("missing helper was reported as applied")
	}
	current, _ := m.lifecycle.Get(r.OperationID)
	if !current.ExplorerShortcut || len(m.shellQueue.pending) != 1 {
		t.Fatal("saved desired visibility lost or duplicated")
	}
}
