package projectionrefresh

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerCoalescesTriggersPerServer(t *testing.T) {
	started := make(chan int, 4)
	finished := make(chan int, 4)
	release := make(chan struct{}, 4)
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32

	scheduler := New(t.Context(), func(_ context.Context, _ string) {
		call := int(calls.Add(1))
		concurrent := active.Add(1)
		for {
			observed := maximum.Load()
			if concurrent <= observed || maximum.CompareAndSwap(observed, concurrent) {
				break
			}
		}
		started <- call
		<-release
		active.Add(-1)
		finished <- call
	})
	t.Cleanup(scheduler.Close)

	if !scheduler.Schedule("spot") {
		t.Fatal("initial Schedule was rejected")
	}
	wantValue(t, started, 1)
	for range 32 {
		if !scheduler.Schedule("spot") {
			t.Fatal("pending Schedule was rejected")
		}
	}
	release <- struct{}{}
	wantValue(t, finished, 1)
	wantValue(t, started, 2)
	release <- struct{}{}
	wantValue(t, finished, 2)

	scheduler.Close()
	if got := calls.Load(); got != 2 {
		t.Fatalf("refresh calls = %d, want 2", got)
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent refreshes for one server = %d, want 1", got)
	}
}

func TestSchedulerPreservesTriggerArrivingDuringCoalescedRerun(t *testing.T) {
	started := make(chan int, 4)
	release := make(chan struct{}, 4)
	var calls atomic.Int32
	scheduler := New(t.Context(), func(_ context.Context, _ string) {
		started <- int(calls.Add(1))
		<-release
	})
	t.Cleanup(scheduler.Close)

	if !scheduler.Schedule("spot") {
		t.Fatal("initial Schedule was rejected")
	}
	wantValue(t, started, 1)
	scheduler.Schedule("spot")
	release <- struct{}{}
	wantValue(t, started, 2)

	scheduler.Schedule("spot")
	release <- struct{}{}
	wantValue(t, started, 3)
	release <- struct{}{}
	scheduler.Close()
	if got := calls.Load(); got != 3 {
		t.Fatalf("refresh calls = %d, want 3", got)
	}
}

func TestSchedulerRefreshesDifferentServersInParallel(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	scheduler := New(t.Context(), func(_ context.Context, serverID string) {
		started <- serverID
		<-release
	})
	t.Cleanup(scheduler.Close)

	if !scheduler.Schedule("spot") || !scheduler.Schedule("cloud") {
		t.Fatal("Schedule rejected a valid server")
	}
	seen := map[string]bool{
		wantAnyValue(t, started): true,
		wantAnyValue(t, started): true,
	}
	if !seen["spot"] || !seen["cloud"] {
		t.Fatalf("started servers = %v, want spot and cloud", seen)
	}
	release <- struct{}{}
	release <- struct{}{}
	scheduler.Close()
}

func TestSchedulerCloseCancelsInflightRefreshAndRejectsNewWork(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	scheduler := New(context.Background(), func(ctx context.Context, _ string) {
		close(started)
		<-ctx.Done()
		close(finished)
	})
	if !scheduler.Schedule("spot") {
		t.Fatal("Schedule was rejected")
	}
	wantSignal(t, started)
	scheduler.Close()
	wantSignal(t, finished)
	if scheduler.Schedule("spot") {
		t.Fatal("Schedule accepted work after Close")
	}
}

func TestSchedulerRejectsInvalidInputAndStopsWithParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	scheduler := New(parent, func(context.Context, string) {})
	t.Cleanup(scheduler.Close)
	for _, serverID := range []string{"", " spot", "spot "} {
		if scheduler.Schedule(serverID) {
			t.Fatalf("Schedule(%q) accepted a non-canonical ID", serverID)
		}
	}
	cancel()
	if scheduler.Schedule("spot") {
		t.Fatal("Schedule accepted work after parent cancellation")
	}

	disabled := New(context.Background(), nil)
	t.Cleanup(disabled.Close)
	if disabled.Schedule("spot") {
		t.Fatal("scheduler without refresh function accepted work")
	}
}

func wantValue(t *testing.T, values <-chan int, want int) {
	t.Helper()
	select {
	case got := <-values:
		if got != want {
			t.Fatalf("value = %d, want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %d", want)
	}
}

func wantAnyValue(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case got := <-values:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for value")
		return ""
	}
}

func wantSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func TestSchedulerDoesNotOverlapSameServerDuringConcurrentScheduling(t *testing.T) {
	var active atomic.Int32
	var overlapped atomic.Bool
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	scheduler := New(t.Context(), func(ctx context.Context, _ string) {
		if active.Add(1) != 1 {
			overlapped.Store(true)
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		active.Add(-1)
	})

	if !scheduler.Schedule("spot") {
		t.Fatal("initial Schedule was rejected")
	}
	wantSignal(t, started)
	var group sync.WaitGroup
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			scheduler.Schedule("spot")
		}()
	}
	group.Wait()
	close(release)
	scheduler.Close()
	if overlapped.Load() {
		t.Fatal("same server was refreshed concurrently")
	}
}
