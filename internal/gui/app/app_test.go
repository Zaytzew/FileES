package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

// --- fake implementations ---

type fakeDaemon struct {
	mu           sync.Mutex
	hello        func(ctx context.Context) (*contract.HelloResult, error)
	systemStatus func(ctx context.Context) (*contract.SystemStatusResult, error)
	repoList     func(ctx context.Context) (*contract.RepoListResult, error)
	repoStatus   func(ctx context.Context, id string) (*contract.RepoStatus, error)
	errorList    func(ctx context.Context, pl contract.ErrorListPayload) (*contract.ErrorListResult, error)
	lock         func(ctx context.Context, id string, paths []string) (string, error)
	unlock       func(ctx context.Context, id string, paths []string) (string, error)
	subscribe    func(ctx context.Context) (<-chan contract.Event, error)
}

func (f *fakeDaemon) Hello(ctx context.Context) (*contract.HelloResult, error) {
	f.mu.Lock()
	fn := f.hello
	f.mu.Unlock()
	if fn == nil {
		return &contract.HelloResult{
			DaemonVersion:    "test",
			ProtocolVersions: []string{contract.Protocol},
			Capabilities:     contract.AllCapabilities,
		}, nil
	}
	return fn(ctx)
}
func (f *fakeDaemon) SystemStatus(ctx context.Context) (*contract.SystemStatusResult, error) {
	f.mu.Lock()
	fn := f.systemStatus
	f.mu.Unlock()
	if fn == nil {
		return &contract.SystemStatusResult{State: "running", UptimeSec: 42}, nil
	}
	return fn(ctx)
}
func (f *fakeDaemon) RepoList(ctx context.Context) (*contract.RepoListResult, error) {
	f.mu.Lock()
	fn := f.repoList
	f.mu.Unlock()
	if fn == nil {
		return &contract.RepoListResult{}, nil
	}
	return fn(ctx)
}
func (f *fakeDaemon) RepoStatus(ctx context.Context, id string) (*contract.RepoStatus, error) {
	f.mu.Lock()
	fn := f.repoStatus
	f.mu.Unlock()
	if fn == nil {
		return &contract.RepoStatus{RepoID: id, State: contract.StateActive, Connectivity: contract.ConnOnline}, nil
	}
	return fn(ctx, id)
}
func (f *fakeDaemon) ErrorList(ctx context.Context, pl contract.ErrorListPayload) (*contract.ErrorListResult, error) {
	f.mu.Lock()
	fn := f.errorList
	f.mu.Unlock()
	if fn == nil {
		return &contract.ErrorListResult{}, nil
	}
	return fn(ctx, pl)
}
func (f *fakeDaemon) Lock(ctx context.Context, id string, paths []string) (string, error) {
	f.mu.Lock()
	fn := f.lock
	f.mu.Unlock()
	if fn == nil {
		return "", nil
	}
	return fn(ctx, id, paths)
}
func (f *fakeDaemon) Unlock(ctx context.Context, id string, paths []string) (string, error) {
	f.mu.Lock()
	fn := f.unlock
	f.mu.Unlock()
	if fn == nil {
		return "", nil
	}
	return fn(ctx, id, paths)
}
func (f *fakeDaemon) Subscribe(ctx context.Context) (<-chan contract.Event, error) {
	f.mu.Lock()
	fn := f.subscribe
	f.mu.Unlock()
	if fn == nil {
		return make(chan contract.Event), nil
	}
	return fn(ctx)
}

// setRepoList replaces the repoList handler concurrently-safely.
func (f *fakeDaemon) setRepoList(fn func(ctx context.Context) (*contract.RepoListResult, error)) {
	f.mu.Lock()
	f.repoList = fn
	f.mu.Unlock()
}

// fakeClock implements Clock with manual Advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock   *fakeClock
	at      time.Time
	fn      func()
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now()}
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) clockTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), fn: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance advances the clock and fires all elapsed timers synchronously.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var toFire []func()
	var remaining []*fakeTimer
	for _, t := range c.timers {
		if t.stopped {
			continue
		}
		if !c.now.Before(t.at) {
			toFire = append(toFire, t.fn)
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
	c.mu.Unlock()
	for _, fn := range toFire {
		fn()
	}
}

// fakeBackoff returns steps[0], steps[1], …, then the last step forever.
type fakeBackoff struct {
	steps []time.Duration
	idx   int
	mu    sync.Mutex
}

func (b *fakeBackoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := b.steps[b.idx]
	if b.idx < len(b.steps)-1 {
		b.idx++
	}
	return d
}
func (b *fakeBackoff) Reset() { b.mu.Lock(); b.idx = 0; b.mu.Unlock() }

// vmCollector collects ViewModels from OnChange in a buffered channel.
type vmCollector struct {
	ch chan ViewModel
}

func newVMCollector() *vmCollector { return &vmCollector{ch: make(chan ViewModel, 64)} }

func (c *vmCollector) onChange(vm ViewModel) { c.ch <- vm }

// waitFor blocks until pred returns true for a received ViewModel or timeout.
func (c *vmCollector) waitFor(t *testing.T, timeout time.Duration, pred func(ViewModel) bool) ViewModel {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case vm := <-c.ch:
			if pred(vm) {
				return vm
			}
		case <-deadline:
			t.Fatal("timeout waiting for expected ViewModel")
			return ViewModel{}
		}
	}
}

func startApp(ctx context.Context, d *fakeDaemon, vc *vmCollector, clock *fakeClock, backoff BackoffSequence) *App {
	cfg := Config{
		Client:   d,
		OnChange: vc.onChange,
		Clock:    clock,
		Backoff:  backoff,
		Debounce: 10 * time.Millisecond,
		Periodic: -1, // disabled in tests unless explicitly exercised
	}
	app := New(cfg)
	go app.Run(ctx)
	return app
}

// --- reducer unit tests ---

func TestReducerApplyConnected(t *testing.T) {
	s := newAppState()
	s = s.applyConnected([]string{contract.CapRepoLock, contract.CapEventsSubscribe})
	if !s.connected || !s.stale {
		t.Fatalf("connected=%v stale=%v", s.connected, s.stale)
	}
	if !s.caps[contract.CapRepoLock] || !s.caps[contract.CapEventsSubscribe] {
		t.Fatalf("caps = %v", s.caps)
	}
}

func TestReducerApplyDisconnected(t *testing.T) {
	s := newAppState().applyConnected([]string{contract.CapRepoLock})
	s = s.applyDisconnected()
	if s.connected || !s.stale {
		t.Fatalf("connected=%v stale=%v", s.connected, s.stale)
	}
	if len(s.caps) != 0 {
		t.Fatalf("caps should be empty after disconnect")
	}
	if s.lastSeq != 0 {
		t.Fatalf("lastSeq should be reset to 0")
	}
}

func TestReducerApplyRepoList(t *testing.T) {
	s := newAppState()
	repos := []contract.RepoSummary{
		{ID: "a", URL: "svn://x/a", LocalPath: "/wc/a", State: contract.StateActive},
		{ID: "b", URL: "svn://x/b", LocalPath: "/wc/b", State: contract.StateInitializing},
	}
	s = s.applyRepoList(repos)
	if len(s.order) != 2 || s.order[0] != "a" || s.order[1] != "b" {
		t.Fatalf("order = %v", s.order)
	}
	if s.summaries["a"].URL != "svn://x/a" {
		t.Fatalf("summary a = %v", s.summaries["a"])
	}
}

func TestReducerApplySnapshot(t *testing.T) {
	s := newAppState().applyRepoList([]contract.RepoSummary{{ID: "a"}})
	snap := contract.RepoStatus{RepoID: "a", State: contract.StateActive, Connectivity: contract.ConnOnline, LocalRevision: 42}
	s = s.applySnapshot(snap)
	if s.snapshots["a"].LocalRevision != 42 {
		t.Fatalf("snapshot not stored: %v", s.snapshots["a"])
	}
	vm := s.viewModel()
	if len(vm.Repos) != 1 || vm.Repos[0].LocalRev != 42 {
		t.Fatalf("viewModel repos = %v", vm.Repos)
	}
}

func TestReducerApplyEventMonotone(t *testing.T) {
	s := newAppState()
	newS, resync, dirty := s.applyEvent(contract.Event{Sequence: 1, RepoID: "a"})
	if resync || dirty != "a" {
		t.Fatalf("first event: resync=%v dirty=%q", resync, dirty)
	}
	_, resync, dirty = newS.applyEvent(contract.Event{Sequence: 2, RepoID: "a"})
	if resync || dirty != "a" {
		t.Fatalf("second event: resync=%v dirty=%q", resync, dirty)
	}
}

func TestReducerApplyEventGap(t *testing.T) {
	s := newAppState()
	s, _, _ = s.applyEvent(contract.Event{Sequence: 1, RepoID: "a"})
	_, resync, dirty := s.applyEvent(contract.Event{Sequence: 3, RepoID: "a"}) // gap: 2 missing
	if !resync || dirty != "" {
		t.Fatalf("gap not detected: resync=%v dirty=%q", resync, dirty)
	}
}

// --- icon aggregation unit tests ---

func TestAggregateIconDisconnected(t *testing.T) {
	if got := aggregateIcon(false, nil); got != IconDisconnected {
		t.Fatalf("got %q, want %q", got, IconDisconnected)
	}
}

func TestAggregateIconActiveWhenEmpty(t *testing.T) {
	if got := aggregateIcon(true, nil); got != IconActive {
		t.Fatalf("got %q, want %q", got, IconActive)
	}
}

func TestAggregateIconPriority(t *testing.T) {
	cases := []struct {
		repos []RepoViewModel
		want  IconState
	}{
		{[]RepoViewModel{{State: contract.StateActive, Connectivity: contract.ConnOnline}}, IconActive},
		{[]RepoViewModel{{State: contract.StateActive, Connectivity: contract.ConnOnline, CurrentOp: ptr("commit")}}, IconBusy},
		{[]RepoViewModel{{State: contract.StateActive, Connectivity: contract.ConnOffline}}, IconOffline},
		{[]RepoViewModel{{State: contract.StateActive, Conflicts: 1}}, IconError},
		{[]RepoViewModel{{State: contract.StateDegraded}}, IconError},
		// highest-priority wins
		{[]RepoViewModel{
			{State: contract.StateActive, Connectivity: contract.ConnOnline},
			{State: contract.StateActive, Connectivity: contract.ConnOffline},
			{State: contract.StateActive, Conflicts: 1},
		}, IconError},
	}
	for _, c := range cases {
		if got := aggregateIcon(true, c.repos); got != c.want {
			t.Errorf("repos=%v: got %q, want %q", c.repos, got, c.want)
		}
	}
}

func ptr(s string) *string { return &s }

// --- runner integration tests ---

func TestAppInitOrderAndConnected(t *testing.T) {
	// Verifies: hello → subscribe → systemStatus → repoList → repoStatus.
	evCh := make(chan contract.Event)
	var callOrder []string
	var mu sync.Mutex
	record := func(s string) { mu.Lock(); callOrder = append(callOrder, s); mu.Unlock() }

	d := &fakeDaemon{
		hello: func(ctx context.Context) (*contract.HelloResult, error) {
			record("hello")
			return &contract.HelloResult{
				DaemonVersion:    "test",
				ProtocolVersions: []string{contract.Protocol},
				Capabilities:     []string{contract.CapEventsSubscribe, contract.CapRepoLock},
			}, nil
		},
		subscribe: func(ctx context.Context) (<-chan contract.Event, error) {
			record("subscribe")
			return evCh, nil
		},
		systemStatus: func(ctx context.Context) (*contract.SystemStatusResult, error) {
			record("systemStatus")
			return &contract.SystemStatusResult{State: "running", UptimeSec: 99}, nil
		},
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			record("repoList")
			return &contract.RepoListResult{Repos: []contract.RepoSummary{
				{ID: "proj", URL: "svn://x/proj", LocalPath: "/wc/proj", State: contract.StateActive},
			}}, nil
		},
		repoStatus: func(ctx context.Context, id string) (*contract.RepoStatus, error) {
			record("repoStatus:" + id)
			return &contract.RepoStatus{RepoID: id, State: contract.StateActive, Connectivity: contract.ConnOnline}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	// Wait until connected and repo appears.
	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && len(vm.Repos) == 1 && vm.Repos[0].State == contract.StateActive
	})
	if vm.Icon != IconActive {
		t.Fatalf("icon = %q, want active", vm.Icon)
	}
	if vm.DaemonState != "running" || vm.UptimeSec != 99 {
		t.Fatalf("system status = %q/%d", vm.DaemonState, vm.UptimeSec)
	}

	mu.Lock()
	order := append([]string{}, callOrder...)
	mu.Unlock()
	if len(order) < 4 || order[0] != "hello" || order[1] != "subscribe" || order[2] != "systemStatus" || order[3] != "repoList" {
		t.Fatalf("call order = %v, want [hello subscribe systemStatus repoList ...]", order)
	}
}

func TestAppMultipleRepos(t *testing.T) {
	d := &fakeDaemon{
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{
				{ID: "a", LocalPath: "/wc/a"},
				{ID: "b", LocalPath: "/wc/b"},
			}}, nil
		},
		repoStatus: func(ctx context.Context, id string) (*contract.RepoStatus, error) {
			state := contract.StateActive
			if id == "b" {
				state = contract.StateOffline
			}
			return &contract.RepoStatus{RepoID: id, State: state, Connectivity: contract.ConnOnline}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && len(vm.Repos) == 2 &&
			vm.Repos[0].State != "" && vm.Repos[1].State != ""
	})
	// Order from repo.list: a then b.
	if vm.Repos[0].ID != "a" || vm.Repos[1].ID != "b" {
		t.Fatalf("repo order = %v %v", vm.Repos[0].ID, vm.Repos[1].ID)
	}
	// b is offline → icon should be at least offline.
	if vm.Icon == IconActive {
		t.Fatalf("icon should not be active when a repo is offline")
	}
}

func TestAppCapabilityGating(t *testing.T) {
	d := &fakeDaemon{
		hello: func(ctx context.Context) (*contract.HelloResult, error) {
			return &contract.HelloResult{
				DaemonVersion:    "test",
				ProtocolVersions: []string{contract.Protocol},
				Capabilities:     []string{contract.CapRepoLock}, // no events.subscribe, no repo.unlock
			}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })
	if !vm.HasCap(contract.CapRepoLock) {
		t.Fatal("expected repo.lock capability")
	}
	if vm.HasCap(contract.CapEventsSubscribe) {
		t.Fatal("events.subscribe should not be present")
	}
	if vm.HasCap(contract.CapRepoUnlock) {
		t.Fatal("repo.unlock should not be present")
	}
}

func TestAppReconnectOnStreamClose(t *testing.T) {
	// Simulate: connect → stream closes → disconnect (icon=disconnected) → reconnect.
	evCh1 := make(chan contract.Event)
	evCh2 := make(chan contract.Event)
	var attempt int
	var mu sync.Mutex

	clock := newFakeClock()
	backoff := &fakeBackoff{steps: []time.Duration{0}} // immediate reconnect when Advance(0) called

	d := &fakeDaemon{
		subscribe: func(ctx context.Context) (<-chan contract.Event, error) {
			mu.Lock()
			attempt++
			a := attempt
			mu.Unlock()
			if a == 1 {
				return evCh1, nil
			}
			return evCh2, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, clock, backoff)

	// Wait for first connection.
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })

	// Close the stream → triggers disconnect.
	close(evCh1)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return !vm.Connected && vm.Stale })

	// Fire the reconnect timer.
	clock.Advance(0)

	// Second connect should succeed.
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })

	mu.Lock()
	att := attempt
	mu.Unlock()
	if att < 2 {
		t.Fatalf("expected at least 2 subscribe attempts, got %d", att)
	}
}

func TestAppResyncOnSequenceGap(t *testing.T) {
	evCh := make(chan contract.Event, 4)
	releaseResync := make(chan struct{})
	var resyncCount int
	var mu sync.Mutex

	d := &fakeDaemon{
		subscribe: func(ctx context.Context) (<-chan contract.Event, error) { return evCh, nil },
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			mu.Lock()
			resyncCount++
			call := resyncCount
			mu.Unlock()
			if call > 1 {
				select {
				case <-releaseResync:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "x"}}}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	// Wait for the initial authoritative snapshot.
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && !vm.Stale })

	mu.Lock()
	before := resyncCount
	mu.Unlock()

	// Send seq=1 then seq=3 (gap at 2) → should trigger resync.
	evCh <- contract.Event{Protocol: contract.Protocol, EventID: "e1", Sequence: 1,
		Timestamp: time.Now().UTC().Format(time.RFC3339), Type: contract.EvOnlineRestored, RepoID: "x"}
	evCh <- contract.Event{Protocol: contract.Protocol, EventID: "e3", Sequence: 3,
		Timestamp: time.Now().UTC().Format(time.RFC3339), Type: contract.EvOnlineRestored, RepoID: "x"}

	// The old snapshot becomes stale immediately and stays stale until the
	// deliberately blocked full resync completes.
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && vm.Stale })
	close(releaseResync)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && !vm.Stale })
	mu.Lock()
	after := resyncCount
	mu.Unlock()
	if after <= before {
		t.Fatal("resync not triggered after sequence gap")
	}
}

func TestAppEventCoalescence(t *testing.T) {
	// Multiple events for the same repo within the debounce window must result
	// in a single RepoStatus call (coalesced), not one per event.
	evCh := make(chan contract.Event, 8)
	var statusCalls int
	var mu sync.Mutex

	clock := newFakeClock()

	d := &fakeDaemon{
		subscribe: func(ctx context.Context) (<-chan contract.Event, error) { return evCh, nil },
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "r"}}}, nil
		},
		repoStatus: func(ctx context.Context, id string) (*contract.RepoStatus, error) {
			if id == "r" {
				mu.Lock()
				statusCalls++
				mu.Unlock()
			}
			return &contract.RepoStatus{RepoID: id, State: contract.StateActive, Connectivity: contract.ConnOnline}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, clock, &fakeBackoff{steps: []time.Duration{time.Hour}})

	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })

	mu.Lock()
	before := statusCalls
	mu.Unlock()

	// Send 5 events for the same repo in rapid succession.
	for i := 1; i <= 5; i++ {
		evCh <- contract.Event{
			Protocol: contract.Protocol, EventID: "e" + string(rune('0'+i)),
			Sequence:  int64(i),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Type:      contract.EvOnlineRestored, RepoID: "r",
		}
	}

	// Let the event loop process the events. Debounce timer not fired yet.
	time.Sleep(30 * time.Millisecond)

	// Fire debounce timer — one flush for all 5 events.
	clock.Advance(50 * time.Millisecond)

	// Wait for at least one status call after the flush.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		after := statusCalls
		mu.Unlock()
		if after > before {
			// Coalesced: only 1 refresh call despite 5 events.
			if after-before > 1 {
				t.Fatalf("expected 1 coalesced refresh, got %d", after-before)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no refresh triggered after debounce")
}

func TestAppSubscribeChannelClosedOnContextCancel(t *testing.T) {
	evCh := make(chan contract.Event)
	d := &fakeDaemon{
		subscribe: func(ctx context.Context) (<-chan contract.Event, error) { return evCh, nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })
	cancel()

	// After cancel the loop exits cleanly — no panic, no goroutine leak.
	// Give it a moment to wind down.
	time.Sleep(50 * time.Millisecond)
}

func TestAppHelloFailureThenReconnect(t *testing.T) {
	var attempt int
	var mu sync.Mutex
	clock := newFakeClock()

	d := &fakeDaemon{
		hello: func(ctx context.Context) (*contract.HelloResult, error) {
			mu.Lock()
			attempt++
			a := attempt
			mu.Unlock()
			if a == 1 {
				return nil, errors.New("connection refused")
			}
			return &contract.HelloResult{
				DaemonVersion:    "test",
				ProtocolVersions: []string{contract.Protocol},
				Capabilities:     contract.AllCapabilities,
			}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, clock, &fakeBackoff{steps: []time.Duration{0}})

	// First connect fails. Observing disconnected guarantees the reconnect
	// timer has already been registered by the event loop.
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return !vm.Connected && vm.Stale })
	clock.Advance(0)

	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })
	mu.Lock()
	att := attempt
	mu.Unlock()
	if att < 2 {
		t.Fatalf("expected 2 hello attempts, got %d", att)
	}
}

func TestAppKeepsSnapshotStaleUntilFullRefreshCompletes(t *testing.T) {
	release := make(chan struct{})
	d := &fakeDaemon{
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "slow"}}}, nil
		},
		repoStatus: func(ctx context.Context, id string) (*contract.RepoStatus, error) {
			select {
			case <-release:
				return &contract.RepoStatus{RepoID: id, State: contract.StateActive, Connectivity: contract.ConnOnline}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })
	if !vm.Stale || vm.Icon != IconBusy {
		t.Fatalf("during initial refresh: stale=%v icon=%q", vm.Stale, vm.Icon)
	}
	close(release)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && len(vm.Repos) == 1
	})
}

func TestAppRepoListFailureDisconnectsAndReconnects(t *testing.T) {
	var calls int
	var mu sync.Mutex
	clock := newFakeClock()
	d := &fakeDaemon{
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			if call == 1 {
				return nil, errors.New("daemon disappeared")
			}
			return &contract.RepoListResult{}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, clock, &fakeBackoff{steps: []time.Duration{0}})

	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return !vm.Connected && vm.Stale })
	clock.Advance(0)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && !vm.Stale })
}

func TestAppPeriodicRefreshDetectsFailureWithoutEventCapability(t *testing.T) {
	clock := newFakeClock()
	var fail bool
	var mu sync.Mutex
	d := &fakeDaemon{
		hello: func(ctx context.Context) (*contract.HelloResult, error) {
			return &contract.HelloResult{ProtocolVersions: []string{contract.Protocol}}, nil
		},
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			mu.Lock()
			shouldFail := fail
			mu.Unlock()
			if shouldFail {
				return nil, errors.New("daemon unavailable")
			}
			return &contract.RepoListResult{}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	app := New(Config{
		Client: d, OnChange: vc.onChange, Clock: clock,
		Backoff:  &fakeBackoff{steps: []time.Duration{time.Hour}},
		Debounce: 10 * time.Millisecond, Periodic: time.Second,
	})
	go app.Run(ctx)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && !vm.Stale })

	mu.Lock()
	fail = true
	mu.Unlock()
	clock.Advance(time.Second)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return !vm.Connected && vm.Stale })
}

func TestAppIgnoresLateSnapshotFromOldGeneration(t *testing.T) {
	clock := newFakeClock()
	events1 := make(chan contract.Event)
	events2 := make(chan contract.Event)
	var generation int
	var mu sync.Mutex
	d := &fakeDaemon{
		subscribe: func(ctx context.Context) (<-chan contract.Event, error) {
			mu.Lock()
			generation++
			gen := generation
			mu.Unlock()
			if gen == 1 {
				return events1, nil
			}
			return events2, nil
		},
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "repo"}}}, nil
		},
		repoStatus: func(ctx context.Context, id string) (*contract.RepoStatus, error) {
			mu.Lock()
			rev := int64(generation)
			mu.Unlock()
			return &contract.RepoStatus{RepoID: id, State: contract.StateActive,
				Connectivity: contract.ConnOnline, LocalRevision: rev}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	application := startApp(ctx, d, vc, clock, &fakeBackoff{steps: []time.Duration{0}})
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && len(vm.Repos) == 1 && vm.Repos[0].LocalRev == 1
	})

	close(events1)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return !vm.Connected })
	clock.Advance(0)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && len(vm.Repos) == 1 && vm.Repos[0].LocalRev == 2
	})

	// No legitimate notifications remain queued at this point.
	for {
		select {
		case <-vc.ch:
			continue
		default:
			goto drained
		}
	}

drained:
	application.msgCh <- msgFullSnapshot{
		gen:       1,
		system:    contract.SystemStatusResult{State: "stale-session"},
		summaries: []contract.RepoSummary{{ID: "repo"}},
		statuses: []contract.RepoStatus{{RepoID: "repo", State: contract.StateActive,
			Connectivity: contract.ConnOnline, LocalRevision: 999}},
	}
	select {
	case vm := <-vc.ch:
		t.Fatalf("old generation changed ViewModel: %#v", vm)
	case <-time.After(100 * time.Millisecond):
	}
}
