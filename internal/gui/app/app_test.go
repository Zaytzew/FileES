package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/projectionmirror"
	contract "filees/pkg/contract/v1"
)

func TestOfflineMirrorRestoresReservationsAsUnverified(t *testing.T) {
	store, err := projectionmirror.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	result := contract.RepoReservationListResult{
		ServerID:     "spot",
		Reservations: []contract.Reservation{{RepoID: "docs", Path: "plan.dwg", Token: "token"}},
		Sources:      []contract.ReservationSource{{RepoID: "docs", State: contract.ReservationSourceFresh, AsOf: asOf, Generation: "8"}},
	}
	raw, _ := json.Marshal(result)
	if err := store.Save("spot", asOf.Add(time.Minute), raw); err != nil {
		t.Fatal(err)
	}
	application := New(Config{Mirror: store, OfflineActivations: []contract.ActivationStatus{{ServerID: "spot", DisplayName: "Spot"}}})
	state := application.loadOfflineMirrors(newAppState())
	vm := state.viewModel()
	if vm.Connected || !vm.Stale || len(vm.Reservations) != 1 || len(vm.Servers) != 1 {
		t.Fatalf("offline mirror view=%+v", vm)
	}
	if !vm.Servers[0].ReservationsKnown || vm.Servers[0].ReservationProjection != string(contract.ReservationSourceFresh) || vm.Servers[0].ReservationAsOf == "" {
		t.Fatalf("offline server=%+v", vm.Servers[0])
	}
}

func TestOfflineMirrorKeepsAnsweredInventoryKnownWithUnknownSource(t *testing.T) {
	store, err := projectionmirror.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	result := contract.RepoReservationListResult{
		ServerID: "spot",
		Sources: []contract.ReservationSource{
			{RepoID: "inactive", State: contract.ReservationSourceUnknown},
			{RepoID: "docs", State: contract.ReservationSourceFresh, AsOf: asOf, Generation: "8"},
		},
	}
	raw, _ := json.Marshal(result)
	if err := store.Save("spot", asOf.Add(time.Minute), raw); err != nil {
		t.Fatal(err)
	}
	application := New(Config{Mirror: store, OfflineActivations: []contract.ActivationStatus{{ServerID: "spot", DisplayName: "Spot"}}})
	vm := application.loadOfflineMirrors(newAppState()).viewModel()
	if len(vm.Servers) != 1 || !vm.Servers[0].ReservationsKnown || vm.Servers[0].ReservationProjection != string(contract.ReservationSourceFresh) {
		t.Fatalf("answered mirror was collapsed to unknown: %+v", vm.Servers)
	}
}

func TestAggregateReservationProjectionPreservesOfflineAndUnknown(t *testing.T) {
	asOf := time.Now().UTC()
	state, stamp := aggregateReservationProjection([]contract.ReservationSource{
		{RepoID: "a", State: contract.ReservationSourceFresh, AsOf: asOf},
		{RepoID: "b", State: contract.ReservationSourceOffline, AsOf: asOf.Add(-time.Minute)},
	}, true)
	if state != string(contract.ReservationSourceOffline) || stamp == "" {
		t.Fatalf("offline aggregate state=%q as_of=%q", state, stamp)
	}
	state, _ = aggregateReservationProjection(nil, false)
	if state != string(contract.ReservationSourceUnknown) {
		t.Fatalf("unknown aggregate=%q", state)
	}
}

// --- fake implementations ---

type fakeDaemon struct {
	mu           sync.Mutex
	hello        func(ctx context.Context) (*contract.HelloResult, error)
	systemStatus func(ctx context.Context) (*contract.SystemStatusResult, error)
	repoList     func(ctx context.Context) (*contract.RepoListResult, error)
	repoStatus   func(ctx context.Context, id string) (*contract.RepoStatus, error)
	errorList    func(ctx context.Context, pl contract.ErrorListPayload) (*contract.ErrorListResult, error)
	activity     func(ctx context.Context, limit int) (*contract.RepoActivityResult, error)
	notices      func(ctx context.Context) (*contract.NoticeListResult, error)
	reservations func(ctx context.Context, serverID string) (*contract.RepoReservationListResult, error)
	publicShares func(ctx context.Context) (*contract.PublicShareListResult, error)
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
func (f *fakeDaemon) RepoActivity(ctx context.Context, limit int) (*contract.RepoActivityResult, error) {
	f.mu.Lock()
	fn := f.activity
	f.mu.Unlock()
	if fn == nil {
		return &contract.RepoActivityResult{}, nil
	}
	return fn(ctx, limit)
}
func (f *fakeDaemon) NoticeList(ctx context.Context) (*contract.NoticeListResult, error) {
	f.mu.Lock()
	fn := f.notices
	f.mu.Unlock()
	if fn == nil {
		return &contract.NoticeListResult{}, nil
	}
	return fn(ctx)
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

func (f *fakeDaemon) RepoReservationList(ctx context.Context, serverID string) (*contract.RepoReservationListResult, error) {
	f.mu.Lock()
	fn := f.reservations
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, serverID)
	}
	return &contract.RepoReservationListResult{}, nil
}
func (f *fakeDaemon) RepoReservationRelease(context.Context, contract.RepoReservationReleasePayload) error {
	return nil
}
func (f *fakeDaemon) PublicShareListAll(ctx context.Context) (*contract.PublicShareListResult, error) {
	f.mu.Lock()
	fn := f.publicShares
	f.mu.Unlock()
	if fn == nil {
		return &contract.PublicShareListResult{}, nil
	}
	return fn(ctx)
}
func (f *fakeDaemon) RealmAliasClaim(context.Context, string, string) (*contract.RealmAliasClaimResult, error) {
	return &contract.RealmAliasClaimResult{Alias: "test"}, nil
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

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
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
		{ID: "a", URL: "svn://x/a", LocalPath: "/wc/a", State: contract.StateActive, LifecycleOperationID: "op-a", LifecycleError: "stuck", CanRetryLifecycle: true, CanAbandonLifecycle: true},
		{ID: "b", URL: "svn://x/b", LocalPath: "/wc/b", State: contract.StateInitializing},
	}
	s = s.applyRepoList(repos)
	if len(s.order) != 2 || s.order[0] != "a" || s.order[1] != "b" {
		t.Fatalf("order = %v", s.order)
	}
	if s.summaries["a"].URL != "svn://x/a" {
		t.Fatalf("summary a = %v", s.summaries["a"])
	}
	vm := s.viewModel()
	if vm.Repos[0].LifecycleOperationID != "op-a" || vm.Repos[0].LifecycleError != "stuck" || !vm.Repos[0].CanRetryLifecycle || !vm.Repos[0].CanAbandonLifecycle {
		t.Fatalf("repair metadata lost by reducer: %+v", vm.Repos[0])
	}
}

func TestReducerRepoSlotsRemainStableAcrossSnapshots(t *testing.T) {
	s := newAppState().applyRepoList([]contract.RepoSummary{{ID: "a"}, {ID: "b"}})
	s = s.applyRepoList([]contract.RepoSummary{{ID: "b"}, {ID: "a"}, {ID: "c"}})
	if got := repoIDs(s.viewModel().Repos); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("reordered slots = %v", got)
	}

	s = s.applyRepoList([]contract.RepoSummary{{ID: "b"}, {ID: "c"}})
	if got := repoIDs(s.viewModel().Repos); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("absent repository left a visible slot = %v", got)
	}

	s = s.applyRepoList([]contract.RepoSummary{{ID: "c"}, {ID: "a"}, {ID: "b"}})
	if got := repoIDs(s.viewModel().Repos); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("returning repository lost its first-seen slot = %v", got)
	}
}

func repoIDs(repos []RepoViewModel) []string {
	ids := make([]string, len(repos))
	for i, repo := range repos {
		ids[i] = repo.ID
	}
	return ids
}

func TestReducerServerSlotsRemainStableAcrossSnapshots(t *testing.T) {
	s := newAppState()
	first := contract.SystemStatusResult{Activations: []contract.ActivationStatus{{ServerID: "spot"}, {ServerID: "manual"}}}
	s.system = first
	s = s.rememberServerOrder(first, nil, nil)
	second := contract.SystemStatusResult{Activations: []contract.ActivationStatus{{ServerID: "manual"}, {ServerID: "spot"}, {ServerID: "cloud"}}}
	s.system = second
	s = s.rememberServerOrder(second, nil, nil)
	servers := s.viewModel().Servers
	got := make([]string, len(servers))
	for i, server := range servers {
		got[i] = server.ID
	}
	if !reflect.DeepEqual(got, []string{"spot", "manual", "cloud"}) {
		t.Fatalf("reordered server slots = %v", got)
	}
}

func TestReducerProjectsPendingActionUntilExplicitFenceCompletion(t *testing.T) {
	started := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	s := newAppState().startPendingAction(PendingAction{ID: "lock:1", Kind: "lock", RepoID: "docs", Label: "Zakładanie blokady", StartedAt: started})
	vm := s.viewModel()
	if len(vm.PendingActions) != 1 || vm.PendingActions[0].Phase != ActionRunning {
		t.Fatalf("started action = %#v", vm.PendingActions)
	}
	s = s.awaitPendingAction("lock:1")
	if got := s.viewModel().PendingActions[0].Phase; got != ActionAwaitingProjection {
		t.Fatalf("awaiting phase = %q", got)
	}
	s = s.finishPendingActions([]string{"lock:1"})
	if got := s.viewModel().PendingActions; len(got) != 0 {
		t.Fatalf("completed action still projected = %#v", got)
	}
}

func TestReducerActionFenceRequiresAuthoritativeExpectedReservationChange(t *testing.T) {
	action := PendingAction{ID: "lock:1", Kind: "lock", RepoID: "docs", ServerID: "spot", ReservationDelta: 1, BaselineReservations: 2, BaselineReservationsKnown: true}
	s := newAppState().startPendingAction(action).awaitPendingAction(action.ID)
	s.repoReservations[reservationKey("spot", "docs")] = 3
	var waiting []string
	s, waiting = s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 1 || len(s.pendingActions) != 1 {
		t.Fatalf("unknown inventory crossed fence: waiting=%v pending=%v", waiting, s.pendingActions)
	}

	s.serverReservationsKnown["spot"] = true
	s.repoReservations[reservationKey("spot", "docs")] = 2
	s, waiting = s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 1 || len(s.pendingActions) != 1 {
		t.Fatalf("unchanged inventory crossed fence: waiting=%v pending=%v", waiting, s.pendingActions)
	}

	s.repoReservations[reservationKey("spot", "docs")] = 3
	s, waiting = s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 || len(s.pendingActionOrder) != 0 {
		t.Fatalf("confirmed inventory did not finish action: waiting=%v pending=%v order=%v", waiting, s.pendingActions, s.pendingActionOrder)
	}
}

func TestReducerRecoveryDismissFenceWaitsForCapabilityToDisappear(t *testing.T) {
	action := PendingAction{ID: "dismiss:1", Kind: "dismiss_recovery", ServerID: "spot", RepoID: "archive", ExpectedRecoveryDismissed: true}
	s := newAppState().applyRepoList([]contract.RepoSummary{{ID: "archive", ServerID: "spot", ServerDeleted: true, RecoveryAvailable: true}}).startPendingAction(action).awaitPendingAction(action.ID)
	s, waiting := s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 1 || len(s.viewModel().PendingActions) != 1 {
		t.Fatalf("dismissal confirmed before projection changed: waiting=%v", waiting)
	}
	s = s.applyRepoList([]contract.RepoSummary{{ID: "archive", ServerID: "spot", ServerDeleted: true}})
	s, waiting = s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 0 || len(s.viewModel().PendingActions) != 0 {
		t.Fatalf("dismissal fence not released: waiting=%v", waiting)
	}
}

func TestReducerFinishesSuccessfulLockWhenInventoryWasAlreadyUnknown(t *testing.T) {
	action := PendingAction{ID: "lock:unknown", Kind: "lock", RepoID: "docs", ServerID: "spot", ReservationDelta: 1, BaselineReservationsKnown: false}
	s := newAppState().startPendingAction(action)
	s = s.awaitPendingAction(action.ID)
	if len(s.pendingActions) != 0 || len(s.pendingActionOrder) != 0 {
		t.Fatalf("unverifiable successful lock retained spinner: pending=%v order=%v", s.pendingActions, s.pendingActionOrder)
	}
}

func TestReducerActionFenceRequiresProjectedSessionTimeout(t *testing.T) {
	action := PendingAction{ID: "session_timeout:1", Kind: "session_timeout", ServerID: "spot", ExpectedSessionTimeoutMin: 90}
	s := newAppState().startPendingAction(action).awaitPendingAction(action.ID)
	s.system = contract.SystemStatusResult{Activations: []contract.ActivationStatus{{ServerID: "spot", SessionTimeoutMin: 30}}}

	s, waiting := s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 1 || len(s.pendingActions) != 1 {
		t.Fatalf("old timeout crossed fence: waiting=%v pending=%v", waiting, s.pendingActions)
	}

	s.system = contract.SystemStatusResult{Activations: []contract.ActivationStatus{{ServerID: "spot", SessionTimeoutMin: 90}}}
	s, waiting = s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 {
		t.Fatalf("projected timeout did not finish action: waiting=%v pending=%v", waiting, s.pendingActions)
	}
}

func TestReducerActionFenceRequiresProjectedRepositoryLifecycle(t *testing.T) {
	attach := PendingAction{ID: "attach:1", Kind: "connect_repositories", ServerID: "spot", RepoID: "docs", ExpectedRepoAttached: true}
	s := newAppState().applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot", Attached: false}})
	s = s.startPendingAction(attach).awaitPendingAction(attach.ID)
	s, waiting := s.confirmPendingActions([]string{attach.ID})
	if len(waiting) != 1 {
		t.Fatalf("unattached repository crossed attach fence: waiting=%v", waiting)
	}
	s = s.applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot", Attached: true}})
	s, waiting = s.confirmPendingActions([]string{attach.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 {
		t.Fatalf("attached repository did not finish action: waiting=%v pending=%v", waiting, s.pendingActions)
	}

	detach := PendingAction{ID: "detach:1", Kind: "detach_repository", ServerID: "spot", RepoID: "docs", ExpectedRepoDetached: true}
	s = newAppState().applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot", Attached: true}})
	s = s.startPendingAction(detach).awaitPendingAction(detach.ID)

	s, waiting = s.confirmPendingActions([]string{detach.ID})
	if len(waiting) != 1 || len(s.pendingActions) != 1 {
		t.Fatalf("attached repository crossed detach fence: waiting=%v pending=%v", waiting, s.pendingActions)
	}
	s = s.applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot", Attached: false}})
	s, waiting = s.confirmPendingActions([]string{detach.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 {
		t.Fatalf("detached repository did not finish action: waiting=%v pending=%v", waiting, s.pendingActions)
	}

	remove := PendingAction{ID: "delete:1", Kind: "delete_repository", ServerID: "spot", RepoID: "docs", ExpectedRepoDeleted: true}
	s = s.applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot", Attached: false}}).startPendingAction(remove).awaitPendingAction(remove.ID)
	s, waiting = s.confirmPendingActions([]string{remove.ID})
	if len(waiting) != 1 {
		t.Fatalf("present repository crossed delete fence: waiting=%v", waiting)
	}
	s = s.applyRepoList(nil)
	s, waiting = s.confirmPendingActions([]string{remove.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 {
		t.Fatalf("removed repository did not finish action: waiting=%v pending=%v", waiting, s.pendingActions)
	}
}

func TestReducerActionFenceWaitsForLifecycleCompletionOrRenewedFailure(t *testing.T) {
	action := PendingAction{ID: "repair:1", Kind: "repair_repository_lifecycle", ServerID: "spot", RepoID: "docs", ExpectedLifecycleOperationID: "op-stuck"}
	s := newAppState().applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot", LifecycleOperationID: "op-stuck"}})
	s = s.startPendingAction(action).awaitPendingAction(action.ID)
	s, waiting := s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 1 || len(s.pendingActions) != 1 {
		t.Fatalf("in-progress repair crossed fence: waiting=%v pending=%v", waiting, s.pendingActions)
	}
	s = s.applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot", LifecycleOperationID: "op-stuck", LifecycleError: "failed again", CanRetryLifecycle: true}})
	s, waiting = s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 {
		t.Fatalf("renewed repair failure kept spinner: waiting=%v pending=%v", waiting, s.pendingActions)
	}

	action.ID = "repair:2"
	s = s.startPendingAction(action).awaitPendingAction(action.ID)
	s = s.applyRepoList([]contract.RepoSummary{{ID: "docs", ServerID: "spot"}})
	s, waiting = s.confirmPendingActions([]string{action.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 {
		t.Fatalf("cleared repair projection kept spinner: waiting=%v pending=%v", waiting, s.pendingActions)
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

func TestReducerPublicSharesChangedRequestsAggregateRefresh(t *testing.T) {
	_, resync, dirty := newAppState().applyEvent(contract.Event{Sequence: 1, Type: contract.EvPublicSharesChanged, RepoID: "docs"})
	if !resync || dirty != "" {
		t.Fatalf("public shares event: resync=%v dirty=%q", resync, dirty)
	}
}

func TestReducerLockReleaseChangedRequestsSystemRefresh(t *testing.T) {
	_, resync, dirty := newAppState().applyEvent(contract.Event{Sequence: 1, Type: contract.EvLockReleaseChanged})
	if !resync || dirty != "" {
		t.Fatalf("lock release event resync=%v dirty=%q", resync, dirty)
	}
}

func TestReducerProjectsLockReleaseRecordWithoutLosingFence(t *testing.T) {
	system := contract.SystemStatusResult{LockReleaseRequests: []contract.LockReleaseRequest{{
		RequestID: "request-1", ServerID: "office", RepoID: "docs", Path: "plans/a.dwg",
		ObservedLockID: "opaque-token", Role: "requester", CounterpartyRealmAlias: "studio",
		State: "pending", CreatedAt: "2026-08-31T10:00:00Z", UpdatedAt: "2026-08-31T10:00:00Z", ExpiresAt: "2026-08-31T13:00:00Z",
	}}}
	s := newAppState().applyFullSnapshot(system, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, time.Now())
	vm := s.viewModel()
	if len(vm.LockReleaseRequests) != 1 {
		t.Fatalf("requests=%+v", vm.LockReleaseRequests)
	}
	got := vm.LockReleaseRequests[0]
	if got.ID != "request-1" || got.ObservedLockID != "opaque-token" || got.Role != "requester" || got.CounterpartyRealmAlias != "studio" {
		t.Fatalf("projected request=%+v", got)
	}
}

// --- icon aggregation unit tests ---

func TestAggregateIconDisconnected(t *testing.T) {
	if got := aggregateIcon(false, nil, 0); got != IconDisconnected {
		t.Fatalf("got %q, want %q", got, IconDisconnected)
	}
}

func TestAggregateIconActiveWhenEmpty(t *testing.T) {
	if got := aggregateIcon(true, nil, 0); got != IconActive {
		t.Fatalf("got %q, want %q", got, IconActive)
	}
}

func TestAggregateIconAnnouncementOverridesRepositoryState(t *testing.T) {
	repos := []RepoViewModel{{State: contract.StateDegraded, Conflicts: 1}}
	if got := aggregateIcon(true, repos, 1); got != IconShout {
		t.Fatalf("got %q, want %q", got, IconShout)
	}
	if got := aggregateIcon(false, repos, 1); got != IconDisconnected {
		t.Fatalf("disconnected got %q, want %q", got, IconDisconnected)
	}
}

func TestReducerKeepsReadAnnouncementHistoryButOnlyUnreadRaisesAlarm(t *testing.T) {
	s := newAppState().applyConnected(contract.AllCapabilities)
	s = s.applyFullSnapshot(contract.SystemStatusResult{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, []contract.Notice{
		{ID: "read", Revision: 7, Title: "przeczytane", Acked: true},
		{ID: "unread", Revision: 8, Title: "nowe"},
	}, nil, false, time.Now())
	vm := s.viewModel()
	if len(vm.Notices) != 2 || !vm.Notices[0].Acked || vm.Notices[0].Revision != 7 || vm.Icon != IconShout {
		t.Fatalf("view model=%+v", vm)
	}
	s = s.applyFullSnapshot(contract.SystemStatusResult{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, []contract.Notice{{ID: "read", Acked: true}}, nil, false, time.Now())
	if vm = s.viewModel(); len(vm.Notices) != 1 || vm.Icon == IconShout {
		t.Fatalf("read-only view model=%+v", vm)
	}
}

func TestReducerProjectsAggregatePublicSharesWithoutLeakingObjectDetails(t *testing.T) {
	revision := int64(17)
	s := newAppState().applyConnected(contract.AllCapabilities)
	s = s.applyFullSnapshot(contract.SystemStatusResult{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, []contract.PublicShareSummary{{
		ChannelID: "channel-1", ServerID: "spot", RepoID: "docs", RepoDisplayName: "Dokumenty",
		Alias: "acme", Slug: "wydanie", State: "active", SourceRoot: "release", UpdatedAt: "2026-08-29T08:00:00Z",
		Recipients: []string{"one@example.test", "two@example.test"}, PasswordProtected: true, DoNotFollow: &revision,
		Objects: []contract.PublicShareObject{{PublicID: "object-1"}, {PublicID: "object-2"}},
	}}, true, time.Now())

	vm := s.viewModel()
	if !vm.PublicSharesKnown || len(vm.PublicShares) != 1 {
		t.Fatalf("public shares = known %v rows %#v", vm.PublicSharesKnown, vm.PublicShares)
	}
	share := vm.PublicShares[0]
	if share.ChannelID != "channel-1" || share.RepoDisplayName != "Dokumenty" || share.RecipientCount != 2 || share.ObjectCount != 2 || !share.PasswordProtected || share.FollowHead {
		t.Fatalf("public share = %#v", share)
	}
}

func TestReducerKeepsLastKnownPublicSharesAcrossSupplementalRefreshFailure(t *testing.T) {
	s := newAppState().applyConnected([]string{contract.CapRepoPublicShareList})
	s = s.applyFullSnapshot(contract.SystemStatusResult{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, []contract.PublicShareSummary{{ChannelID: "known", ServerID: "spot", RepoID: "docs"}}, true, time.Now())
	s = s.applyFullSnapshot(contract.SystemStatusResult{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, time.Now())
	if vm := s.viewModel(); !vm.PublicSharesKnown || len(vm.PublicShares) != 1 || vm.PublicShares[0].ChannelID != "known" {
		t.Fatalf("last known public shares were discarded: %#v", vm.PublicShares)
	}
	s = s.applyConnected(nil)
	if vm := s.viewModel(); vm.PublicSharesKnown || len(vm.PublicShares) != 0 {
		t.Fatalf("unsupported daemon retained aggregate public shares: %#v", vm.PublicShares)
	}
}

func TestAggregateIconPriority(t *testing.T) {
	cases := []struct {
		repos []RepoViewModel
		want  IconState
	}{
		{[]RepoViewModel{{State: contract.StateActive, Connectivity: contract.ConnOnline}}, IconActive},
		{[]RepoViewModel{{State: contract.StateUnattached, Connectivity: contract.ConnOnline}}, IconActive},
		{[]RepoViewModel{{State: contract.StateInitializing, Connectivity: contract.ConnOnline}}, IconBusy},
		{[]RepoViewModel{{State: contract.StateInitializing, Connectivity: contract.ConnOnline, AttachmentPolicy: "optional"}}, IconActive},
		{[]RepoViewModel{{State: contract.StateInitializing, Connectivity: contract.ConnOnline, AttachmentPolicy: "optional", LocalPath: `/wc/import`}}, IconBusy},
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
		if got := aggregateIcon(true, c.repos, 0); got != c.want {
			t.Errorf("repos=%v: got %q, want %q", c.repos, got, c.want)
		}
	}
}

func TestRepoDisplayStateHidesProtocolVocabulary(t *testing.T) {
	operation := "commit"
	cases := []struct {
		repo RepoViewModel
		want RepoDisplayState
	}{
		{RepoViewModel{State: contract.StateActive}, RepoDisplayActive},
		{RepoViewModel{State: contract.StateActive, CurrentOp: &operation}, RepoDisplayBusy},
		{RepoViewModel{State: contract.StateInitializing}, RepoDisplayInitializing},
		{RepoViewModel{State: contract.StateBaselining}, RepoDisplayBaselining},
		{RepoViewModel{State: contract.StatePaused}, RepoDisplayPaused},
		{RepoViewModel{State: contract.StateStopping}, RepoDisplayStopping},
		{RepoViewModel{State: contract.StateActive, Connectivity: contract.ConnOffline}, RepoDisplayOffline},
		{RepoViewModel{State: contract.StateDegraded}, RepoDisplayAttention},
		{RepoViewModel{State: "future-state"}, RepoDisplayUnknown},
	}
	for _, tc := range cases {
		if got := tc.repo.DisplayState(); got != tc.want {
			t.Errorf("repo=%#v: got %q, want %q", tc.repo, got, tc.want)
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

func TestAppFullRefreshIncludesStructuredErrorsAndTimestamp(t *testing.T) {
	clock := newFakeClock()
	wantRefresh := clock.Now()
	d := &fakeDaemon{
		errorList: func(ctx context.Context, payload contract.ErrorListPayload) (*contract.ErrorListResult, error) {
			if payload.Limit != 20 {
				t.Fatalf("error.list limit = %d, want 20", payload.Limit)
			}
			return &contract.ErrorListResult{Errors: []contract.ErrorRecord{{
				ID: "err-1", TS: "2026-07-13T20:30:00Z", RepoID: "repo",
				Code: "NET-4007", Severity: "WARN", Hint: "retry", Msg: "offline",
				Details: "must not cross the presentation boundary",
			}}}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, clock, &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && len(vm.Errors) == 1
	})
	if !vm.LastRefresh.Equal(wantRefresh) {
		t.Fatalf("last refresh = %v, want %v", vm.LastRefresh, wantRefresh)
	}
	want := ErrorViewModel{ID: "err-1", RepoID: "repo", Timestamp: "2026-07-13T20:30:00Z",
		Code: "NET-4007", Severity: "WARN", Hint: "retry", Message: "offline"}
	if vm.Errors[0] != want {
		t.Fatalf("error view model = %#v, want %#v", vm.Errors[0], want)
	}
}

func TestAppFullRefreshIncludesAggregatePublicShares(t *testing.T) {
	d := &fakeDaemon{
		hello: func(context.Context) (*contract.HelloResult, error) {
			return &contract.HelloResult{DaemonVersion: "test", ProtocolVersions: []string{contract.Protocol}, Capabilities: []string{contract.CapRepoPublicShareList}}, nil
		},
		publicShares: func(context.Context) (*contract.PublicShareListResult, error) {
			return &contract.PublicShareListResult{Shares: []contract.PublicShareSummary{{
				ChannelID: "share-1", ServerID: "spot", RepoID: "docs", Alias: "acme", Slug: "release", State: "active",
			}}}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && vm.PublicSharesKnown && len(vm.PublicShares) == 1
	})
	if vm.PublicShares[0].ChannelID != "share-1" || vm.PublicShares[0].ServerID != "spot" {
		t.Fatalf("public share = %#v", vm.PublicShares[0])
	}
}

func TestAppKeepsHealthySessionWhenOlderDaemonRejectsAggregatePublicShares(t *testing.T) {
	d := &fakeDaemon{
		hello: func(context.Context) (*contract.HelloResult, error) {
			return &contract.HelloResult{DaemonVersion: "test", ProtocolVersions: []string{contract.Protocol}, Capabilities: []string{contract.CapRepoPublicShareList}}, nil
		},
		publicShares: func(context.Context) (*contract.PublicShareListResult, error) {
			return nil, errors.New("unknown command repo.public_share_list_all")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && !vm.Stale && vm.DaemonState == "running"
	})
	if vm.PublicSharesKnown || len(vm.PublicShares) != 0 {
		t.Fatalf("unsupported aggregate should stay unknown: known %v rows %#v", vm.PublicSharesKnown, vm.PublicShares)
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

func TestAppProjectsAuthoritativeReservationRowsAlongsideCounts(t *testing.T) {
	d := &fakeDaemon{
		systemStatus: func(context.Context) (*contract.SystemStatusResult, error) {
			return &contract.SystemStatusResult{State: "running", Activations: []contract.ActivationStatus{{ServerID: "office", DisplayName: "Biuro"}}}, nil
		},
		repoList: func(context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "docs", ServerID: "office", LocalPath: "/wc/docs", Attached: true}}}, nil
		},
		repoStatus: func(context.Context, string) (*contract.RepoStatus, error) {
			return &contract.RepoStatus{RepoID: "docs", ServerID: "office", State: contract.StateActive, Connectivity: contract.ConnOnline}, nil
		},
		reservations: func(_ context.Context, serverID string) (*contract.RepoReservationListResult, error) {
			return &contract.RepoReservationListResult{ServerID: serverID, Reservations: []contract.Reservation{{
				RepoID: "docs", WorkingCopy: "/wc/docs", Path: "plan.dwg", Token: "opaque-token", CanRelease: true,
			}}}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && len(vm.Reservations) == 1 && len(vm.Repos) == 1 && vm.Repos[0].ReservationCount == 1
	})
	reservation := vm.Reservations[0]
	if reservation.ID == "" || reservation.ServerID != "office" || reservation.Token != "opaque-token" || !reservation.CanRelease {
		t.Fatalf("reservation = %+v", reservation)
	}
}

func TestAppKeepsHealthyServerReservationsWhenAnotherServerFails(t *testing.T) {
	d := &fakeDaemon{
		systemStatus: func(context.Context) (*contract.SystemStatusResult, error) {
			return &contract.SystemStatusResult{State: "running", Activations: []contract.ActivationStatus{
				{ServerID: "spot", DisplayName: "spot"},
				{ServerID: "cloud", DisplayName: "cloud"},
			}}, nil
		},
		repoList: func(context.Context) (*contract.RepoListResult, error) {
			return &contract.RepoListResult{Repos: []contract.RepoSummary{{ID: "docs", ServerID: "spot", LocalPath: "/wc/docs", Attached: true}}}, nil
		},
		repoStatus: func(context.Context, string) (*contract.RepoStatus, error) {
			return &contract.RepoStatus{RepoID: "docs", ServerID: "spot", State: contract.StateActive, Connectivity: contract.ConnOnline}, nil
		},
		reservations: func(_ context.Context, serverID string) (*contract.RepoReservationListResult, error) {
			if serverID == "cloud" {
				return nil, errors.New("LOCK-2101 cloud unavailable")
			}
			return &contract.RepoReservationListResult{ServerID: serverID, Reservations: []contract.Reservation{{
				RepoID: "docs", WorkingCopy: "/wc/docs", Path: "plan.dwg", Token: "spot-lock",
			}}}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && len(vm.Servers) == 2 && len(vm.Reservations) == 1
	})
	servers := make(map[string]ServerViewModel, len(vm.Servers))
	for _, server := range vm.Servers {
		servers[server.ID] = server
	}
	if !servers["spot"].ReservationsKnown || servers["spot"].ReservationCount != 1 {
		t.Fatalf("healthy server projection = %+v", servers["spot"])
	}
	if servers["cloud"].ReservationsKnown || servers["cloud"].ReservationCount != 0 {
		t.Fatalf("failed server projection = %+v", servers["cloud"])
	}
	if !vm.CanBrowseReservations() {
		t.Fatal("healthy server reservation became unavailable globally")
	}
}

func TestAppKeepsAnsweredReservationInventoryKnownWithUnknownSource(t *testing.T) {
	d := &fakeDaemon{
		systemStatus: func(context.Context) (*contract.SystemStatusResult, error) {
			return &contract.SystemStatusResult{State: "running", Activations: []contract.ActivationStatus{{ServerID: "spot", DisplayName: "spot"}}}, nil
		},
		reservations: func(_ context.Context, serverID string) (*contract.RepoReservationListResult, error) {
			return &contract.RepoReservationListResult{ServerID: serverID, Sources: []contract.ReservationSource{
				{RepoID: "inactive", State: contract.ReservationSourceUnknown},
				{RepoID: "docs", State: contract.ReservationSourceFresh, AsOf: time.Now().UTC()},
			}}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})

	vm := vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return vm.Connected && len(vm.Servers) == 1
	})
	if !vm.Servers[0].ReservationsKnown || vm.Servers[0].ReservationProjection != string(contract.ReservationSourceFresh) {
		t.Fatalf("answered inventory was collapsed to unknown: %+v", vm.Servers[0])
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
		errorList: func(context.Context, contract.ErrorListPayload) (*contract.ErrorListResult, error) {
			t.Fatal("error.list called without advertised capability")
			return nil, nil
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

func TestAppManualReconnectStartsFreshSession(t *testing.T) {
	events := []chan contract.Event{make(chan contract.Event), make(chan contract.Event)}
	var attempts int
	var mu sync.Mutex
	d := &fakeDaemon{
		subscribe: func(context.Context) (<-chan contract.Event, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			return events[attempts-1], nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	a := New(Config{
		Client:   d,
		OnChange: vc.onChange,
		Clock:    newFakeClock(),
		Backoff:  &fakeBackoff{steps: []time.Duration{time.Hour}},
		Periodic: -1,
	})
	go a.Run(ctx)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })

	a.Reconnect()
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return !vm.Connected && vm.Stale })
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected })

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 2 {
		t.Fatalf("subscribe attempts = %d, want 2", got)
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

	// Connecting also launches a full refresh, and that refresh calls RepoStatus
	// itself. Waiting only for Connected races it: the connect-time call can
	// still be in flight when statusCalls is sampled below, and would then be
	// counted against the debounce flush, making a perfectly coalesced flush
	// look like two refreshes. Wait for the repo to actually reach the view
	// model instead - that happens only once the connect-time refresh has
	// completed and been applied.
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && len(vm.Repos) == 1 })

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

func TestActionBadgeWaitsForPostActionFullSnapshot(t *testing.T) {
	refreshStarted := make(chan struct{}, 1)
	releaseRefresh := make(chan struct{})
	var calls int
	var mu sync.Mutex
	d := &fakeDaemon{
		repoList: func(ctx context.Context) (*contract.RepoListResult, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			if call > 1 {
				select {
				case refreshStarted <- struct{}{}:
				default:
				}
				select {
				case <-releaseRefresh:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &contract.RepoListResult{}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := newVMCollector()
	application := startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && !vm.Stale })

	action := PendingAction{ID: "lock:1", Kind: "lock", Label: "Zakładanie blokady", StartedAt: time.Now()}
	if !application.StartAction(action) {
		t.Fatal("action start was not queued")
	}
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return len(vm.PendingActions) == 1 && vm.PendingActions[0].Phase == ActionRunning
	})
	application.AwaitActionProjection(action.ID)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool {
		return len(vm.PendingActions) == 1 && vm.PendingActions[0].Phase == ActionAwaitingProjection
	})
	select {
	case <-refreshStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("post-action refresh did not start")
	}
	close(releaseRefresh)
	vc.waitFor(t, 3*time.Second, func(vm ViewModel) bool { return vm.Connected && len(vm.PendingActions) == 0 })
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
