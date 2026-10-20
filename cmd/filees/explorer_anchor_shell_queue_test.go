//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"filees/pkg/commit"
	"filees/pkg/localrepo"
)

func TestShellQueueFIFOAfterBusyAndDeduplication(t *testing.T) {
	var q anchorShellQueue
	a := localrepo.Record{OperationID: "A", LocalPath: "old"}
	b := localrepo.Record{OperationID: "B"}
	if !q.enqueue(a) || !q.enqueue(b) || q.enqueue(a) {
		t.Fatal("duplicate changed FIFO")
	}
	for _, wantErr := range []error{commit.ErrLocalStatusBusy, errors.New("helper failed")} {
		more, err := q.runOne(t.Context(), func(_ context.Context, r localrepo.Record) error {
			if r.OperationID != "A" {
				t.Fatal("head overtaken")
			}
			return wantErr
		})
		if !more || !errors.Is(err, wantErr) {
			t.Fatal(more, err)
		}
	}
	for _, want := range []string{"A", "B"} {
		_, err := q.runOne(t.Context(), func(_ context.Context, r localrepo.Record) error {
			if r.OperationID != want {
				t.Fatalf("want %s, got %s", want, r.OperationID)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(q.pending) != 0 {
		t.Fatal("completed entries retained")
	}
}

func TestShellQueueSingleConsumerAndStaleLocation(t *testing.T) {
	var q anchorShellQueue
	a := localrepo.Record{OperationID: "A", LocalPath: "old"}
	q.enqueue(a)
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, _ = q.runOne(t.Context(), func(context.Context, localrepo.Record) error {
			close(started)
			<-finish
			return errAnchorShellStale
		})
	}()
	<-started
	if q.enqueue(a) {
		t.Error("in-flight duplicate appended")
	}
	a.LocalPath = "new"
	if !q.enqueue(a) {
		t.Error("new location lost")
	}
	_, _ = q.runOne(t.Context(), func(context.Context, localrepo.Record) error {
		t.Error("second consumer entered")
		return nil
	})
	close(finish)
	<-done
	_, err := q.runOne(t.Context(), func(_ context.Context, r localrepo.Record) error {
		if r.LocalPath != "new" {
			t.Error("stale cancellation lost replacement")
		}
		return nil
	})
	if err != nil || len(q.pending) != 0 {
		t.Fatal("replacement not completed", err)
	}
}

func TestShellQueueCancellationRetainsHead(t *testing.T) {
	var q anchorShellQueue
	q.enqueue(localrepo.Record{OperationID: "A"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	more, err := q.runOne(ctx, func(context.Context, localrepo.Record) error {
		t.Fatal("cancelled consumer ran")
		return nil
	})
	if !more || !errors.Is(err, context.Canceled) || len(q.pending) != 1 {
		t.Fatal(more, err)
	}
}

func TestShellAdmissionYieldsObserversAndRespectsUserMoves(t *testing.T) {
	mu := new(sync.Mutex)
	m := &anchorManager{localIntentMu: mu}
	mu.Lock()
	done := make(chan error, 1)
	go func() {
		leave, err := m.enterShellIntent(t.Context())
		if err == nil {
			leave()
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for m.shellWaiters.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !m.observerMustYield() {
		mu.Unlock()
		<-done
		t.Fatal("observer ignores FIFO admission")
	}
	m.moveWaiters.Store(1)
	mu.Unlock()
	if err := <-done; !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	if m.shellWaiters.Load() != 0 {
		t.Fatal("admission priority leaked")
	}
	m.moveWaiters.Store(0)
	leave, err := m.enterShellIntent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	leave()
}

func TestShellAdmissionOutlivesAnAlreadyActiveObserver(t *testing.T) {
	mu := new(sync.Mutex)
	m := &anchorManager{localIntentMu: mu}
	mu.Lock()
	released := make(chan struct{})
	go func() { time.Sleep(650 * time.Millisecond); mu.Unlock(); close(released) }()
	leave, err := m.enterShellIntent(t.Context())
	if err == nil {
		leave()
	}
	<-released
	if err != nil {
		t.Fatal("active metadata turn starved FIFO admission", err)
	}
}
