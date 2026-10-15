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
	var active, peak, remoteActive, remotePeak atomic.Int32
	var wg sync.WaitGroup
	for i := range 120 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := t.Context()
			if i%3 == 0 {
				ctx = WithInteractiveRead(ctx)
			}
			remote := i%2 == 0
			release, e := b.acquire(ctx, remote)
			if e != nil {
				t.Error(e)
				return
			}
			n := active.Add(1)
			if remote {
				n := remoteActive.Add(1)
				for old := remotePeak.Load(); n > old && !remotePeak.CompareAndSwap(old, n); old = remotePeak.Load() {
				}
			}
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			if remote {
				remoteActive.Add(-1)
			}
			release()
		}()
	}
	wg.Wait()
	if peak.Load() > 4 || peak.Load() < 2 {
		t.Fatal(peak.Load())
	}
	if remotePeak.Load() > 2 || remotePeak.Load() < 1 {
		t.Fatal("remote peak", remotePeak.Load())
	}
	if len(b.all) != 0 || len(b.remote) != 0 {
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

func waitNativeQueue(t *testing.T, b *nativeProcessBudget, class, size int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.waiting[class])
		b.mu.Unlock()
		if n == size {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("class %d did not reach queue size %d", class, size)
}

func queueNative(t *testing.T, b *nativeProcessBudget, interactive bool) <-chan func() {
	t.Helper()
	ctx := t.Context()
	class := 0
	if interactive {
		ctx = WithInteractiveRead(ctx)
		class = 1
	}
	b.mu.Lock()
	size := len(b.waiting[class]) + 1
	b.mu.Unlock()
	result := make(chan func(), 1)
	go func() {
		release, err := b.acquire(ctx, true)
		if err != nil {
			t.Error(err)
			return
		}
		result <- release
	}()
	waitNativeQueue(t, b, class, size)
	return result
}

func takeNative(t *testing.T, ch <-chan func()) func() {
	t.Helper()
	select {
	case release := <-ch:
		return release
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not complete")
		return nil
	}
}

func TestNativeInteractiveFIFOAndBoundedBackgroundWait(t *testing.T) {
	b := newNativeProcessBudget()
	fixed, _ := b.acquire(t.Context(), true)
	defer fixed()
	next, _ := b.acquire(t.Context(), true)
	var background, interactive []<-chan func()
	for range 3 {
		background = append(background, queueNative(t, b, false))
	}
	for range 12 {
		interactive = append(interactive, queueNative(t, b, true))
	}
	// Even an earlier background backlog yields to the interactive lane, but
	// every fourth eligible admission is background. FIFO holds in both lanes.
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			next()
			next = takeNative(t, interactive[i*3+j])
		}
		next()
		next = takeNative(t, background[i])
	}
	for i := 9; i < 12; i++ {
		next()
		next = takeNative(t, interactive[i])
	}
	next()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.waiting[0])+len(b.waiting[1]) != 0 || len(b.remote) != 1 {
		t.Fatal("queue/permit leak")
	}
}

func TestNativeInteractiveCancellationAndLocalCapacity(t *testing.T) {
	b := newNativeProcessBudget()
	a, _ := b.acquire(t.Context(), true)
	defer a()
	d, _ := b.acquire(t.Context(), true)
	ctx, cancel := context.WithCancel(WithInteractiveRead(t.Context()))
	done := make(chan error, 1)
	go func() {
		release, e := b.acquire(ctx, true)
		if release != nil {
			release()
		}
		done <- e
	}()
	waitNativeQueue(t, b, 1, 1)
	bg := queueNative(t, b, false)
	// Queued interactive remote reads cannot block local inspections.
	l1, e := b.acquire(t.Context(), false)
	if e != nil {
		t.Fatal(e)
	}
	l2, e := b.acquire(t.Context(), false)
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	l1()
	l2()
	d()
	takeNative(t, bg)()
	if len(b.all) != 1 || len(b.remote) != 1 {
		t.Fatal("canceled read leaked permit")
	}
}

func TestNativeInteractiveCancelGrantRace(t *testing.T) {
	for range 100 {
		b := newNativeProcessBudget()
		a, _ := b.acquire(t.Context(), true)
		d, _ := b.acquire(t.Context(), true)
		ctx, cancel := context.WithCancel(WithInteractiveRead(t.Context()))
		done := make(chan struct{})
		go func() {
			defer close(done)
			release, _ := b.acquire(ctx, true)
			if release != nil {
				release()
				release()
			}
		}()
		waitNativeQueue(t, b, 1, 1)
		go cancel()
		a()
		<-done
		d()
		b.mu.Lock()
		clean := len(b.all) == 0 && len(b.remote) == 0 && len(b.waiting[0])+len(b.waiting[1]) == 0
		b.mu.Unlock()
		if !clean {
			t.Fatal("cancel/grant leaked permits or waiter")
		}
	}
}

func TestNativeInteractiveRealHelperBypassesBackgroundBacklog(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
	a, _ := nativeProcesses.acquire(t.Context(), true)
	defer a()
	d, _ := nativeProcesses.acquire(t.Context(), true)
	bg := queueNative(t, nativeProcesses, false)
	ctx := context.WithValue(WithInteractiveRead(t.Context()), nativeRemoteContextKey{}, true)
	done := make(chan error, 1)
	go func() { _, e := c.nativeCommand(ctx, "", 10*time.Second, "--version"); done <- e }()
	waitNativeQueue(t, nativeProcesses, 1, 1)
	d()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("interactive child did not run")
	}
	takeNative(t, bg)()
}

func TestNativeInteractiveListKeepsPriorityForBothRemotePhases(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":1,"entries":[]}`)
	fixed, _ := nativeProcesses.acquire(t.Context(), true)
	defer fixed()
	slot, _ := nativeProcesses.acquire(t.Context(), true)
	bg1 := queueNative(t, nativeProcesses, false)
	bg2 := queueNative(t, nativeProcesses, false)
	done := make(chan error, 1)
	go func() { _, err := c.ListImmediate(WithInteractiveRead(t.Context()), "file:///lab"); done <- err }()
	waitNativeQueue(t, nativeProcesses, 1, 1)
	slot()
	// After info exits, the first background job can start while the read
	// prepares its next phase. The list must retain priority over bg2.
	releaseBG := takeNative(t, bg1)
	waitNativeQueue(t, nativeProcesses, 1, 1)
	releaseBG()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("second listing phase lost priority")
	}
	takeNative(t, bg2)()
}
