package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeProcessBudgetBoundsParallelWork(t *testing.T) {
	b := newNativeProcessBudget()
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, e := b.acquire(t.Context(), false)
			if e != nil {
				t.Error(e)
				return
			}
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if peak.Load() > 4 || peak.Load() < 2 {
		t.Fatal(peak.Load())
	}
	if len(b.all) != 0 {
		t.Fatal("leaked slots")
	}
}

func TestNativeProcessBudgetRemoteLeavesLocalSlotsAndCancellation(t *testing.T) {
	b := newNativeProcessBudget()
	a, _ := b.acquire(t.Context(), true)
	defer a()
	d, _ := b.acquire(t.Context(), true)
	defer d()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, e := b.acquire(ctx, true); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	l1, e := b.acquire(t.Context(), false)
	if e != nil {
		t.Fatal(e)
	}
	l2, e := b.acquire(t.Context(), false)
	if e != nil {
		t.Fatal(e)
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel2()
	if _, e := b.acquire(ctx2, false); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	l1()
	l2()
	if len(b.all) != 2 || len(b.remote) != 2 {
		t.Fatal("canceled wait leaked slots")
	}
}

func TestNativeProcessBudgetCanceledRemoteReturnsPartialPermit(t *testing.T) {
	b := newNativeProcessBudget()
	for range 4 {
		r, e := b.acquire(t.Context(), false)
		if e != nil {
			t.Fatal(e)
		}
		defer r()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, e := b.acquire(ctx, true); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if len(b.remote) != 0 {
		t.Fatal("remote slot leaked")
	}
}

func TestNativeCommandWaitCancellationDoesNotLaunch(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
	trace := filepath.Join(t.TempDir(), "versions")
	t.Setenv("FILEES_TEST_VERSION_TRACE", trace)
	for range 4 {
		release, e := nativeProcesses.acquire(t.Context(), false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, e := c.nativeCommand(ctx, "", time.Second, "--version")
	if !errors.Is(e, context.DeadlineExceeded) || IsNetworkError(e) {
		t.Fatalf("queue wait classified as network: %v", e)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("child launched while waiting: %v", err)
	}
}

func TestNativeCommandReturnsSlotsOnBadReceiptAndSuccess(t *testing.T) {
	c := raFake(t, `not json`)
	ctx := context.WithValue(t.Context(), nativeRemoteContextKey{}, true)
	if _, err := c.nativeCommand(ctx, "", 10*time.Second, "status"); err == nil {
		t.Fatal("bad receipt accepted")
	}
	if len(nativeProcesses.all) != 0 || len(nativeProcesses.remote) != 0 {
		t.Fatal("failed child leaked slot")
	}
	if _, err := c.nativeCommand(ctx, "", 10*time.Second, "--version"); err != nil {
		t.Fatal(err)
	}
	if len(nativeProcesses.all) != 0 || len(nativeProcesses.remote) != 0 {
		t.Fatal("successful child leaked slot")
	}
}
