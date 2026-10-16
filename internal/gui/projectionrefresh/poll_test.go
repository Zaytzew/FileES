package projectionrefresh

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollJoinsReadWithoutPerpetualRerun(t *testing.T) {
	started := make(chan int, 4)
	release := make(chan struct{}, 4)
	var calls atomic.Int32
	s := New(t.Context(), func(ctx context.Context, _ string) {
		started <- int(calls.Add(1))
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	t.Cleanup(s.Close)
	if !s.Poll("cloud") {
		t.Fatal("initial poll rejected")
	}
	wantValue(t, started, 1)
	var group sync.WaitGroup
	for range 256 {
		group.Add(1)
		go func() { defer group.Done(); s.Poll("cloud") }()
	}
	group.Wait()
	s.mu.Lock()
	pending := s.servers["cloud"].pending
	s.mu.Unlock()
	if pending {
		t.Fatal("observation hints queued another full read")
	}
	release <- struct{}{}
	waitPollIdle(t, s)
	if calls.Load() != 1 {
		t.Fatal("polls created a repeat")
	}
	// The next ordinary tick still reads; no cached success or extra TTL.
	s.Poll("cloud")
	wantValue(t, started, 2)
	s.Schedule("cloud") // actual change while this read is in flight
	for range 256 {
		s.Poll("cloud")
	}
	s.mu.Lock()
	pending = s.servers["cloud"].pending
	s.mu.Unlock()
	if !pending {
		t.Fatal("poll erased a real invalidation")
	}
	release <- struct{}{}
	wantValue(t, started, 3)
	release <- struct{}{}
	waitPollIdle(t, s)
	if calls.Load() != 3 {
		t.Fatal("invalidation was lost or duplicated")
	}
}

func waitPollIdle(t *testing.T, s *Scheduler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		idle := len(s.servers) == 0
		s.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scheduler did not become idle")
}

func TestPollValidatesAndCancelsLikeSchedule(t *testing.T) {
	var nilScheduler *Scheduler
	if nilScheduler.Poll("cloud") {
		t.Fatal("nil accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	s := New(ctx, func(ctx context.Context, _ string) { close(started); <-ctx.Done() })
	t.Cleanup(s.Close)
	for _, id := range []string{"", " cloud", "cloud "} {
		if s.Poll(id) {
			t.Fatal("invalid ID accepted")
		}
	}
	s.Poll("cloud")
	wantSignal(t, started)
	cancel()
	s.Close()
	if s.Poll("cloud") {
		t.Fatal("closed accepted")
	}
	disabled := New(t.Context(), nil)
	defer disabled.Close()
	if disabled.Poll("cloud") {
		t.Fatal("disabled accepted")
	}
}
