package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func waitAdmissionClosed(t *testing.T, a *Admission) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !a.Snapshot().Closed {
		if time.Now().After(deadline) {
			t.Fatal("admission not closed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAdmissionWaitsForWholeOperationAndRejectsNewWork(t *testing.T) {
	var a Admission
	release, err := a.Enter()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		resume func()
		err    error
	}
	done := make(chan result, 1)
	go func() { resume, err := a.Quiesce(ctx); done <- result{resume, err} }()
	waitAdmissionClosed(t, &a)
	if _, err := a.Enter(); !errors.Is(err, ErrQuiescing) {
		t.Fatalf("enter: %v", err)
	}
	select {
	case <-done:
		t.Fatal("drained with an active write")
	default:
	}
	release()
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if s := a.Snapshot(); s.Active != 0 || !s.Closed {
		t.Fatalf("state: %+v", s)
	}
	if _, err := a.Enter(); !errors.Is(err, ErrQuiescing) {
		t.Fatal("admission reopened before resume")
	}
	r.resume()
	next, err := a.Enter()
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestAdmissionCancelledDrainLeavesOperationAlive(t *testing.T) {
	var a Admission
	release, _ := a.Enter()
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := a.Quiesce(ctx); done <- err }()
	waitAdmissionClosed(t, &a)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s := a.Snapshot(); s.Active != 1 || s.Closed {
		t.Fatalf("state: %+v", s)
	}
	next, err := a.Enter()
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestAdmissionConcurrentDrainCannotReleaseOtherBarrier(t *testing.T) {
	var a Admission
	resume, err := a.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Quiesce(context.Background()); !errors.Is(err, ErrQuiescing) {
		t.Fatal(err)
	}
	resume()
	resumeNext, err := a.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resumeNext()
	resume() // stale, already-used release must not reopen the newer drain
	if !a.Snapshot().Closed {
		t.Fatal("stale resume reopened new barrier")
	}
}

func TestAdmissionReleaseIsConcurrentAndIdempotent(t *testing.T) {
	var a Admission
	release, _ := a.Enter()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); release() }()
	}
	wg.Wait()
	if a.Snapshot().Active != 0 {
		t.Fatal(a.Snapshot())
	}
}

func TestAdmissionEnterRaceCannotCrossCompletedDrain(t *testing.T) {
	for round := 0; round < 100; round++ {
		var a Admission
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, e := a.Enter()
				if e == nil {
					r()
				} else if !errors.Is(e, ErrQuiescing) {
					t.Error(e)
				}
			}()
		}
		close(start)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resume, err := a.Quiesce(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		wg.Wait()
		if s := a.Snapshot(); s.Active != 0 || !s.Closed {
			t.Fatal(s)
		}
		resume()
		cancel()
	}
}

func TestAdmissionCancelledBeforeAttemptDoesNotClose(t *testing.T) {
	var a Admission
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Quiesce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if a.Snapshot().Closed {
		t.Fatal("cancelled attempt closed admission")
	}
}

func TestAdmissionWaiterResumesOnlyWhenReopened(t *testing.T) {
	var a Admission
	resume, err := a.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		release, err := a.EnterContext(ctx)
		if err == nil {
			close(entered)
			release()
		}
		done <- err
	}()
	select {
	case <-entered:
		t.Fatal("waiter crossed closed barrier")
	case <-time.After(20 * time.Millisecond):
	}
	resume()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := a.Snapshot(); s.Active != 0 || s.Closed {
		t.Fatal(s)
	}
}

func TestAdmissionCancelledWaiterDoesNotCountAsWork(t *testing.T) {
	var a Admission
	resume, err := a.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := a.EnterContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if s := a.Snapshot(); s.Active != 0 || !s.Closed {
		t.Fatal(s)
	}
}
