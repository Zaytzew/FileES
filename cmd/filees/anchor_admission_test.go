package main

import (
	"context"
	"errors"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnchorAdmissionWaitsAndRechecksTheSelectedFolder(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		mu.Lock()
		var waiters atomic.Int32
		service := repositoryLifecycleService{store: store, localIntentMu: &mu, anchorAdmissionWaiters: &waiters}
		path := filepath.Join(t.TempDir(), "point")
		done := make(chan error, 1)
		go func() { _, err := service.BeginAnchorAttach("spot", "repo", path, false); done <- err }()
		deadline := time.Now().Add(time.Second)
		for waiters.Load() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("waiter missing")
			}
			time.Sleep(time.Millisecond)
		}
		if len(store.List()) != 0 {
			t.Fatal("intent recorded before admission")
		}
		if dirty {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		mu.Unlock()
		select {
		case err := <-done:
			if (err != nil) != dirty {
				t.Fatal(dirty, err)
			}
		case <-time.After(time.Second):
			t.Fatal("admission did not resume")
		}
		if (len(store.List()) == 1) != (!dirty) || waiters.Load() != 0 {
			t.Fatal("wrong record or leaked priority")
		}
		if !mu.TryLock() {
			t.Fatal("mutex leaked")
		}
		mu.Unlock()
	}
}

func TestAnchorAdmissionCancelAndDeadlineHaveNoEffect(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
	var waiters atomic.Int32
	service := repositoryLifecycleService{localIntentMu: &mu, anchorAdmissionWaiters: &waiters}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.waitForAnchorTurn(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer stop()
	if _, err := service.waitForAnchorTurn(ctx); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	if waiters.Load() != 0 || mu.TryLock() {
		t.Fatal("changed held mutex or leaked waiter")
	}
}

func TestAnchorAdmissionChecksCurrentAuthorizationAfterWaiting(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	mu.Lock()
	var waiters atomic.Int32
	var allowed atomic.Bool
	allowed.Store(true)
	service := repositoryLifecycleService{store: store, localIntentMu: &mu, anchorAdmissionWaiters: &waiters, anchorAdmissionCheck: func(string, string) error {
		if !allowed.Load() {
			return errors.New("access withdrawn")
		}
		return nil
	}}
	done := make(chan error, 1)
	go func() {
		_, err := service.BeginAnchorAttach("spot", "repo", filepath.Join(t.TempDir(), "point"), false)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for waiters.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("waiter absent")
		}
		time.Sleep(time.Millisecond)
	}
	allowed.Store(false)
	mu.Unlock()
	if err := <-done; err == nil || len(store.List()) != 0 {
		t.Fatal("changed authority recorded intent", err)
	}
}
