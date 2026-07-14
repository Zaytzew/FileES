package actions_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

// ---- helpers ----------------------------------------------------------------

type vmStore struct {
	mu sync.RWMutex
	vm app.ViewModel
}

func (s *vmStore) Store(vm app.ViewModel) {
	s.mu.Lock()
	s.vm = vm
	s.mu.Unlock()
}

func (s *vmStore) Load() app.ViewModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vm
}

func vmWithLock(repos ...app.RepoViewModel) app.ViewModel {
	return app.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoLock:   true,
			contract.CapRepoUnlock: true,
		},
		Repos: repos,
	}
}

func repo(id, path string) app.RepoViewModel {
	return app.RepoViewModel{ID: id, LocalPath: path, State: contract.StateActive}
}

// fakeLockUnlocker records calls and signals via channels.
type fakeLockUnlocker struct {
	lockCh    chan lockCall
	unlockCh  chan lockCall
	lockErr   error
	unlockErr error
}

type fakeStructuredError struct{}

func (fakeStructuredError) Error() string { return "wire fallback" }
func (fakeStructuredError) PresentationError() (string, string, string, string) {
	return "LOCK-2001", "ERROR", "REQUIRE_ACTION", "lock.operation_failed"
}

type lockCall struct {
	repoID string
	paths  []string
}

func newFakeLocker() *fakeLockUnlocker {
	return &fakeLockUnlocker{
		lockCh:   make(chan lockCall, 1),
		unlockCh: make(chan lockCall, 1),
	}
}

func (f *fakeLockUnlocker) Lock(_ context.Context, repoID string, paths []string) (string, error) {
	f.lockCh <- lockCall{repoID: repoID, paths: append([]string(nil), paths...)}
	return "", f.lockErr
}

func (f *fakeLockUnlocker) Unlock(_ context.Context, repoID string, paths []string) (string, error) {
	f.unlockCh <- lockCall{repoID: repoID, paths: append([]string(nil), paths...)}
	return "", f.unlockErr
}

// gatedPicker blocks until the test releases it, signalling when it is called.
type gatedPicker struct {
	called chan struct{}
	allow  chan struct{}
	result platform.PickFilesResult
	err    error
}

func newGatedPicker(result platform.PickFilesResult) *gatedPicker {
	return &gatedPicker{
		called: make(chan struct{}, 1), // buffered: non-blocking send succeeds before awaitCh receives
		allow:  make(chan struct{}),
		result: result,
	}
}

func (p *gatedPicker) PickFiles(ctx context.Context, _ platform.PickFilesRequest) (platform.PickFilesResult, error) {
	select {
	case p.called <- struct{}{}:
	default:
	}
	select {
	case <-p.allow:
		return p.result, p.err
	case <-ctx.Done():
		return platform.PickFilesResult{}, ctx.Err()
	}
}

func awaitCh[T any](t *testing.T, ch <-chan T, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s", msg)
		var zero T
		return zero
	}
}

func assertNotReceived[T any](t *testing.T, ch <-chan T, msg string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected receive: %s", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func setup(cfg actions.Config) (chan<- tray.Intent, context.CancelFunc) {
	intents := make(chan tray.Intent, 4)
	cfg.Intents = intents
	ctx, cancel := context.WithCancel(context.Background())
	go actions.New(cfg).Run(ctx)
	return intents, cancel
}

func send(t *testing.T, ch chan<- tray.Intent, intent tray.Intent) {
	t.Helper()
	select {
	case ch <- intent:
	case <-time.After(time.Second):
		t.Fatal("timeout sending intent")
	}
}

// ---- OpenFolder tests -------------------------------------------------------

func TestControllerOpenFolderCallsOpener(t *testing.T) {
	fake := &platformtest.Fake{}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    newFakeLocker(),
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentOpenFolder, RepoID: "repo1"})

	var opened []string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := fake.Snapshot(); len(s.OpenedFolders) > 0 {
			opened = s.OpenedFolders
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(opened) != 1 || opened[0] != "/wc/repo1" {
		t.Fatalf("opened folders = %v", opened)
	}
}

func TestControllerOpenFolderSkipsUnknownRepo(t *testing.T) {
	fake := &platformtest.Fake{}
	vm := &vmStore{}
	vm.Store(vmWithLock())

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    newFakeLocker(),
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentOpenFolder, RepoID: "missing"})
	time.Sleep(50 * time.Millisecond)
	if s := fake.Snapshot(); len(s.OpenedFolders) != 0 {
		t.Fatalf("opener called for unknown repo: %v", s.OpenedFolders)
	}
}

func TestControllerOpenFolderSkipsEmptyLocalPath(t *testing.T) {
	fake := &platformtest.Fake{}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    newFakeLocker(),
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentOpenFolder, RepoID: "repo1"})
	time.Sleep(50 * time.Millisecond)
	if s := fake.Snapshot(); len(s.OpenedFolders) != 0 {
		t.Fatalf("opener called for empty path: %v", s.OpenedFolders)
	}
}

func TestControllerOpenFolderNotifiesOnError(t *testing.T) {
	notifCh := make(chan platform.Notification, 1)
	fake := &platformtest.Fake{
		OpenFolderFunc: func(_ context.Context, _ string) error {
			return errors.New("access denied")
		},
		NotifyFunc: func(_ context.Context, n platform.Notification) error {
			notifCh <- n
			return nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    newFakeLocker(),
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentOpenFolder, RepoID: "repo1"})
	n := awaitCh(t, notifCh, "error notification")
	if n.Title == "" {
		t.Fatalf("notification title empty: %#v", n)
	}
}

// ---- Lock / Unlock tests ----------------------------------------------------

func TestControllerLockHappyPath(t *testing.T) {
	locker := newFakeLocker()
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, req platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{Paths: []string{req.Root + "/file.dwg"}}, nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	call := awaitCh(t, locker.lockCh, "Lock call")
	if call.repoID != "repo1" || len(call.paths) != 1 {
		t.Fatalf("lock call = %#v", call)
	}
}

func TestControllerUnlockHappyPath(t *testing.T) {
	locker := newFakeLocker()
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, req platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{Paths: []string{req.Root + "/file.dwg"}}, nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentUnlock, RepoID: "repo1"})
	call := awaitCh(t, locker.unlockCh, "Unlock call")
	if call.repoID != "repo1" {
		t.Fatalf("unlock call = %#v", call)
	}
}

func TestControllerLockPickerCancellationSkipsDaemon(t *testing.T) {
	locker := newFakeLocker()
	fake := &platformtest.Fake{} // default: cancelled
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	assertNotReceived(t, locker.lockCh, "Lock must not be called after picker cancel")
}

func TestControllerLockCapabilityGatingPreventsPickerAndDaemon(t *testing.T) {
	locker := newFakeLocker()
	pickerCalled := make(chan struct{}, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, _ platform.PickFilesRequest) (platform.PickFilesResult, error) {
			pickerCalled <- struct{}{}
			return platform.PickFilesResult{Cancelled: true}, nil
		},
	}
	vm := &vmStore{}
	vm.Store(app.ViewModel{Connected: true, Capabilities: map[string]bool{}}) // no lock cap

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	assertNotReceived(t, pickerCalled, "picker must not be called without lock capability")
	assertNotReceived(t, locker.lockCh, "Lock must not be called without lock capability")
}

func TestControllerLockStaleStatePreventsPickerAndDaemon(t *testing.T) {
	locker := newFakeLocker()
	pickerCalled := make(chan struct{}, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, _ platform.PickFilesRequest) (platform.PickFilesResult, error) {
			pickerCalled <- struct{}{}
			return platform.PickFilesResult{Cancelled: true}, nil
		},
	}
	vm := &vmStore{}
	stale := vmWithLock(repo("repo1", "/wc/repo1"))
	stale.Stale = true
	vm.Store(stale)

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	assertNotReceived(t, pickerCalled, "picker must not be called for stale state")
	assertNotReceived(t, locker.lockCh, "Lock must not be called for stale state")
}

func TestControllerLockCapabilityRevokedWhilePickerOpen(t *testing.T) {
	locker := newFakeLocker()
	gated := newGatedPicker(platform.PickFilesResult{Paths: []string{"/wc/repo1/file.dwg"}})
	fake := &platformtest.Fake{} // for OpenFolder, Notify
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    gated,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})

	// Wait for the picker goroutine to reach PickFiles.
	awaitCh(t, gated.called, "picker called")

	// Revoke lock capability while the picker is blocked.
	vm.Store(app.ViewModel{Connected: false})

	// Allow the picker to return paths.
	close(gated.allow)

	// Lock must not be called because the second ViewModel check fails.
	assertNotReceived(t, locker.lockCh, "Lock must not be called after capability revoked")
}

func TestControllerLockRepoPathChangedWhilePickerOpen(t *testing.T) {
	locker := newFakeLocker()
	gated := newGatedPicker(platform.PickFilesResult{Paths: []string{"/wc/repo1/file.dwg"}})
	fake := &platformtest.Fake{}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    gated,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	awaitCh(t, gated.called, "picker called")
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1-moved")))
	close(gated.allow)

	assertNotReceived(t, locker.lockCh, "Lock must not use a picker result from the old repo root")
}

func TestControllerLockRejectsPathOutsideRepo(t *testing.T) {
	locker := newFakeLocker()
	notifCh := make(chan platform.Notification, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, _ platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{Paths: []string{"/wc/repo-other/file.dwg"}}, nil
		},
		NotifyFunc: func(_ context.Context, n platform.Notification) error {
			notifCh <- n
			return nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	n := awaitCh(t, notifCh, "invalid path notification")
	if n.Title != "Nieprawidłowy wybór plików" {
		t.Fatalf("notification = %#v", n)
	}
	assertNotReceived(t, locker.lockCh, "Lock must not receive an outside-root path")
}

func TestControllerLockPlatformUnavailableIsNotified(t *testing.T) {
	locker := newFakeLocker()
	notifCh := make(chan platform.Notification, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, _ platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{}, platform.NewUnavailable("file_picker", errors.New("zenity not found"))
		},
		NotifyFunc: func(_ context.Context, n platform.Notification) error {
			select {
			case notifCh <- n:
			default:
			}
			return nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})

	// A visible action must have visible feedback when the platform picker is absent.
	n := awaitCh(t, notifCh, "unavailable picker notification")
	if n.Title == "" {
		t.Fatalf("notification title empty: %#v", n)
	}
	assertNotReceived(t, locker.lockCh, "Lock must not be called")
}

func TestControllerLockPlatformOperationalErrorNotifies(t *testing.T) {
	locker := newFakeLocker()
	notifCh := make(chan platform.Notification, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, _ platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{}, platform.NewOperationalFailure("file_picker", errors.New("dialog crashed"))
		},
		NotifyFunc: func(_ context.Context, n platform.Notification) error {
			notifCh <- n
			return nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	n := awaitCh(t, notifCh, "operational error notification")
	if n.Title == "" {
		t.Fatalf("notification title empty: %#v", n)
	}
}

func TestControllerLockDaemonErrorNotifies(t *testing.T) {
	locker := newFakeLocker()
	locker.lockErr = errors.New("svn: locked by other user")
	notifCh := make(chan platform.Notification, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, req platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{Paths: []string{req.Root + "/file.dwg"}}, nil
		},
		NotifyFunc: func(_ context.Context, n platform.Notification) error {
			notifCh <- n
			return nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	awaitCh(t, locker.lockCh, "Lock call")
	n := awaitCh(t, notifCh, "error notification")
	if n.Title == "" {
		t.Fatalf("notification title empty: %#v", n)
	}
}

func TestControllerLockStructuredDaemonErrorNotifiesSafely(t *testing.T) {
	locker := newFakeLocker()
	locker.lockErr = fakeStructuredError{}
	notifCh := make(chan platform.Notification, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, req platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{Paths: []string{req.Root + "/file.dwg"}}, nil
		},
		NotifyFunc: func(_ context.Context, n platform.Notification) error {
			notifCh <- n
			return nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	awaitCh(t, locker.lockCh, "Lock call")
	n := awaitCh(t, notifCh, "structured error notification")
	if n.Title != "Błąd operacji (lock) — LOCK-2001" || n.Body != "Daemon nie wykonał operacji na plikach — wymagane działanie użytkownika" {
		t.Fatalf("notification = %#v", n)
	}
	if n.Urgency != platform.UrgencyCritical {
		t.Fatalf("urgency = %q", n.Urgency)
	}
}

func TestControllerLockSuccessNotifies(t *testing.T) {
	locker := newFakeLocker()
	notifCh := make(chan platform.Notification, 1)
	fake := &platformtest.Fake{
		PickFilesFunc: func(_ context.Context, req platform.PickFilesRequest) (platform.PickFilesResult, error) {
			return platform.PickFilesResult{Paths: []string{req.Root + "/a.dwg", req.Root + "/b.dwg"}}, nil
		},
		NotifyFunc: func(_ context.Context, n platform.Notification) error {
			notifCh <- n
			return nil
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	awaitCh(t, locker.lockCh, "Lock call")
	n := awaitCh(t, notifCh, "success notification")
	if n.Urgency != platform.UrgencyLow {
		t.Fatalf("success notification urgency = %v", n.Urgency)
	}
}

// ---- Reconnect / Quit -------------------------------------------------------

func TestControllerReconnectCallsCallback(t *testing.T) {
	done := make(chan struct{}, 1)
	fake := &platformtest.Fake{}
	vm := &vmStore{}

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Locker:    newFakeLocker(),
		Reconnect: func() { done <- struct{}{} },
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentReconnect})
	awaitCh(t, done, "Reconnect callback")
}

func TestControllerQuitCallsCallback(t *testing.T) {
	done := make(chan struct{}, 1)
	fake := &platformtest.Fake{}
	vm := &vmStore{}

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Locker:    newFakeLocker(),
		Quit:      func() { done <- struct{}{} },
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentQuit})
	awaitCh(t, done, "Quit callback")
}

// ---- Lifecycle tests --------------------------------------------------------

func TestControllerExitsOnContextCancellation(t *testing.T) {
	fake := &platformtest.Fake{}
	vm := &vmStore{}
	intents := make(chan tray.Intent)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		actions.New(actions.Config{
			Intents:   intents,
			ViewModel: vm.Load,
			Opener:    fake,
			Picker:    fake,
			Locker:    newFakeLocker(),
		}).Run(ctx)
	}()
	cancel()
	awaitCh(t, done, "Run to return after context cancel")
}

func TestControllerExitsWhenIntentsChannelClosed(t *testing.T) {
	fake := &platformtest.Fake{}
	vm := &vmStore{}
	intents := make(chan tray.Intent)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		actions.New(actions.Config{
			Intents:   intents,
			ViewModel: vm.Load,
			Opener:    fake,
			Picker:    fake,
			Locker:    newFakeLocker(),
		}).Run(ctx)
	}()
	close(intents)
	awaitCh(t, done, "Run to return after channel close")
}

func TestControllerConcurrentIntentsAreAllProcessed(t *testing.T) {
	// Two Lock intents with a blocking picker must both be dispatched to the
	// daemon (goroutine-per-intent; Run loop must not be blocked by either).
	locker := &fakeLockUnlocker{
		lockCh:   make(chan lockCall, 2),
		unlockCh: make(chan lockCall, 2),
	}
	gated1 := newGatedPicker(platform.PickFilesResult{Paths: []string{"/wc/r1/a.dwg"}})
	gated2 := newGatedPicker(platform.PickFilesResult{Paths: []string{"/wc/r2/b.dwg"}})
	fake := &platformtest.Fake{
		PickFilesFunc: func(ctx context.Context, req platform.PickFilesRequest) (platform.PickFilesResult, error) {
			if req.Root == "/wc/r1" {
				return gated1.PickFiles(ctx, req)
			}
			if req.Root == "/wc/r2" {
				return gated2.PickFiles(ctx, req)
			}
			return platform.PickFilesResult{}, errors.New("unexpected picker root: " + req.Root)
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("r1", "/wc/r1"), repo("r2", "/wc/r2")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    fake,
		Picker:    fake,
		Notifier:  fake,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "r1"})
	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "r2"})

	// Both pickers must have been called before either is released.
	awaitCh(t, gated1.called, "picker 1 called")
	awaitCh(t, gated2.called, "picker 2 called")

	close(gated1.allow)
	close(gated2.allow)

	awaitCh(t, locker.lockCh, "Lock call 1")
	awaitCh(t, locker.lockCh, "Lock call 2")
}

func TestControllerSerializesLockUnlockPerRepo(t *testing.T) {
	locker := newFakeLocker()
	gated := newGatedPicker(platform.PickFilesResult{Paths: []string{"/wc/r1/a.dwg"}})
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("r1", "/wc/r1")))

	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load,
		Opener:    &platformtest.Fake{},
		Picker:    gated,
		Locker:    locker,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "r1"})
	awaitCh(t, gated.called, "first picker called")
	send(t, intents, tray.Intent{Kind: tray.IntentUnlock, RepoID: "r1"})
	assertNotReceived(t, gated.called, "second picker for busy repo")
	close(gated.allow)
	awaitCh(t, locker.lockCh, "Lock call")
	assertNotReceived(t, locker.unlockCh, "concurrent Unlock for busy repo")
}

func TestControllerContextCancelledDuringPickerAbortsDaemonCall(t *testing.T) {
	locker := newFakeLocker()
	ctx, cancel := context.WithCancel(context.Background())
	intents := make(chan tray.Intent, 1)
	pickerStarted := make(chan struct{})
	fake := &platformtest.Fake{
		PickFilesFunc: func(innerCtx context.Context, _ platform.PickFilesRequest) (platform.PickFilesResult, error) {
			close(pickerStarted)
			<-innerCtx.Done()
			return platform.PickFilesResult{}, innerCtx.Err()
		},
	}
	vm := &vmStore{}
	vm.Store(vmWithLock(repo("repo1", "/wc/repo1")))

	done := make(chan struct{})
	go func() {
		defer close(done)
		actions.New(actions.Config{
			Intents:   intents,
			ViewModel: vm.Load,
			Opener:    fake,
			Picker:    fake,
			Locker:    locker,
		}).Run(ctx)
	}()

	intents <- tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"}
	<-pickerStarted
	cancel()

	awaitCh(t, done, "Run to return")
	assertNotReceived(t, locker.lockCh, "Lock must not be called after ctx cancel")
}
