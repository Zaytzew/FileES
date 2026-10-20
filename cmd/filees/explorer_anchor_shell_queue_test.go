//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/commit"
	"filees/pkg/localrepo"
)

func TestShellQueueProviderLeaseFencesStartAndDrainsAfterEOF(t *testing.T) {
	var q anchorShellQueue
	r := localrepo.Record{OperationID: "A", LocalPath: "root"}
	p, finished, err := q.beginProvider(r)
	if err != nil {
		t.Fatal(err)
	}
	q.enqueue(r)
	if _, _, err = q.beginProvider(r); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal("queued repair admitted replacement", err)
	}
	var stops atomic.Int32
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.drainProvider(ctx, r) }()
	// A stop requested during process startup must not be lost.
	deadline := time.Now().Add(time.Second)
	for !p.stopRequested() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.setStop(func() { stops.Add(1) })
	if stops.Load() != 1 {
		t.Fatal("startup stop lost")
	}
	select {
	case err := <-done:
		t.Fatal("EOF acknowledgement mistaken for completed bridge/cache drain", err)
	default:
	}
	finished()
	finished() // idempotent release, only one close
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, err = q.runOne(t.Context(), func(context.Context, localrepo.Record) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, finishNext, err := q.beginProvider(r)
	if err != nil {
		t.Fatal(err)
	}
	finishNext()
}

func TestShellQueueDrainCancellationPreservesLeaseAndPending(t *testing.T) {
	var q anchorShellQueue
	r := localrepo.Record{OperationID: "A", LocalPath: "root"}
	p, finish, err := q.beginProvider(r)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	q.enqueue(r)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err = q.drainProvider(ctx, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !p.stopRequested() || len(q.pending) != 1 || len(q.providers) != 1 {
		t.Fatal("timeout discarded pending/live provider")
	}
	other := r
	other.OperationID = "new-owner"
	if err = q.drainProvider(t.Context(), other); !errors.Is(err, errAnchorShellStale) {
		t.Fatal("different owner accepted", err)
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if err = q.wait(cancelled, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestShellQueueWaitWakesOnCompletionAndRejectsReenqueueBeforeStart(t *testing.T) {
	var q anchorShellQueue
	r := localrepo.Record{OperationID: "A", LocalPath: "root"}
	q.enqueue(r)
	done := make(chan error, 1)
	go func() { done <- q.wait(t.Context(), r) }()
	_, err := q.runOne(t.Context(), func(context.Context, localrepo.Record) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	q.enqueue(r) // between wait completion and start admission
	if _, _, err = q.beginProvider(r); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal("enqueue/start race", err)
	}
}

func TestShellQueueKeepCannotConsumeAnotherRootsHead(t *testing.T) {
	var q anchorShellQueue
	a := localrepo.Record{OperationID: "A", LocalPath: "A"}
	b := localrepo.Record{OperationID: "B", LocalPath: "B"}
	q.enqueue(a)
	q.enqueue(b)
	_, err := q.runFor(t.Context(), &b, func(context.Context, localrepo.Record) error {
		t.Fatal("B's lifetime executed A's registration")
		return nil
	})
	if err != nil || len(q.pending) != 2 {
		t.Fatal("wrong consumer changed head", err)
	}
	_, err = q.runFor(t.Context(), &a, func(_ context.Context, r localrepo.Record) error {
		if r.OperationID != "A" {
			t.Fatal(r)
		}
		return nil
	})
	if err != nil || len(q.pending) != 1 || q.pending[0].OperationID != "B" {
		t.Fatal("own head not consumed", err)
	}
}

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
