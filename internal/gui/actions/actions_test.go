package actions_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localpin"
)

// ---- helpers ----------------------------------------------------------------

// wcPath turns a POSIX-style fixture path into one that is absolute on the
// host platform. The controller hands every picked path to
// platform.ValidatePickedPaths, which requires filepath.IsAbs; on Windows
// "/wc/repo1" is rooted but drive-relative, so IsAbs rejects it and the
// controller returned before ever calling Lock/Unlock -- the tests then sat
// waiting for a call that could not come and failed on timeout, which read
// like a concurrency bug rather than a fixture problem.
//
// On every other platform this is the identity function, so the fixtures and
// their expectations are byte-for-byte what they were.
// An empty path stays empty: "no local path" is a meaningful fixture value
// (TestControllerOpenFolderSkipsEmptyLocalPath), and filepath.Join would turn
// it into a bare "C:\".
func wcPath(posix string) string {
	if runtime.GOOS != "windows" || posix == "" {
		return posix
	}
	return filepath.Join(`C:\`, filepath.FromSlash(posix))
}

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
	return app.RepoViewModel{ID: id, Access: contract.AccessReadWrite, LocalPath: wcPath(path), State: contract.StateActive, ReservationCount: 1}
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
func (fakeStructuredError) PresentationDetails() map[string]string { return nil }

type fakeStructuredLocateError struct{}

func (fakeStructuredLocateError) Error() string { return "wire locate" }
func (fakeStructuredLocateError) PresentationError() (string, string, string, string) {
	return "REPO-2010", "ERROR", "REQUIRE_ACTION", "repo.locate_failed"
}
func (fakeStructuredLocateError) PresentationDetails() map[string]string { return nil }

type fakeStructuredAttachError struct{}

func (fakeStructuredAttachError) Error() string { return "wire fallback" }
func (fakeStructuredAttachError) PresentationError() (string, string, string, string) {
	return "REPO-2002", "ERROR", "REQUIRE_ACTION", "repo.invalid_local_intent"
}
func (fakeStructuredAttachError) PresentationDetails() map[string]string { return nil }

type lockCall struct {
	repoID string
	paths  []string
}

type reservationCall struct {
	payload app.ReservationReleaseRequest
}

type fakeReservations struct {
	items   []app.Reservation
	listCh  chan string
	release chan reservationCall
}

func newFakeReservations(items []app.Reservation) *fakeReservations {
	return &fakeReservations{items: items, listCh: make(chan string, 4), release: make(chan reservationCall, 8)}
}

func (f *fakeReservations) ListReservations(_ context.Context, serverID string) ([]app.Reservation, error) {
	f.listCh <- serverID
	return append([]app.Reservation(nil), f.items...), nil
}

func (f *fakeReservations) ReleaseReservation(_ context.Context, payload app.ReservationReleaseRequest) error {
	f.release <- reservationCall{payload: payload}
	return nil
}

type fakeActivator struct {
	begins     chan string
	finishes   chan string
	resumes    chan string
	pending    []actions.ActivationTarget
	pendingErr error
	beginErr   error
	finishErr  error
	resumeErr  error
}

func (f *fakeActivator) Pending(_ context.Context) ([]actions.ActivationTarget, error) {
	return append([]actions.ActivationTarget(nil), f.pending...), f.pendingErr
}

type fakeRealmAliases struct {
	mu      sync.Mutex
	errs    []error
	aliases chan string
}

func (f *fakeRealmAliases) ClaimAlias(_ context.Context, _ string, alias string) error {
	f.aliases <- alias
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

type createCall struct{ serverID, displayName, localPath string }
type fakeRepositoryCreator struct {
	calls chan createCall
	// statusFunc controls CreationStatus's reply; nil means "attached" with
	// no error, so tests that don't care about the outcome poll never block
	// on it or emit an unwanted extra notification before ctx is cancelled.
	statusFunc        func(operationID string) (state, lastError string, err error)
	statusContextFunc func(context.Context, string) (state, lastError string, err error)
}

type attachCall struct{ serverID, repoID, localPath string }
type fakeRepositoryAttacher struct {
	calls chan attachCall
	err   error
}

type locateCall struct{ serverID, repoID, localPath string }
type fakeRepositoryLocator struct {
	calls     chan locateCall
	err       error
	status    string
	lastError string
	statusErr error
}

func (f *fakeRepositoryLocator) LocateRepository(_ context.Context, serverID, repoID, localPath string) (string, error) {
	f.calls <- locateCall{serverID: serverID, repoID: repoID, localPath: localPath}
	return "locate-" + repoID, f.err
}

func (f *fakeRepositoryLocator) LocateStatus(_ context.Context, _ string) (string, string, error) {
	if f.statusErr != nil {
		return "", "", f.statusErr
	}
	state := f.status
	if state == "" {
		state = "attached"
	}
	return state, f.lastError, nil
}

func (f *fakeRepositoryAttacher) AttachRepository(_ context.Context, serverID, repoID, localPath string) (string, error) {
	f.calls <- attachCall{serverID: serverID, repoID: repoID, localPath: localPath}
	return "attach-" + repoID, f.err
}

func (f *fakeRepositoryAttacher) AttachmentStatus(_ context.Context, _ string) (string, string, error) {
	return "attached", "", nil
}

func (f *fakeRepositoryCreator) CreateRepository(_ context.Context, serverID, displayName, localPath string) (string, error) {
	f.calls <- createCall{serverID, displayName, localPath}
	return "op-123", nil
}

func (f *fakeRepositoryCreator) CreationStatus(ctx context.Context, operationID string) (string, string, error) {
	if f.statusContextFunc != nil {
		return f.statusContextFunc(ctx, operationID)
	}
	if f.statusFunc != nil {
		return f.statusFunc(operationID)
	}
	return "attached", "", nil
}

func (f *fakeActivator) Begin(_ context.Context, invitation string) (actions.ActivationTarget, error) {
	f.begins <- invitation
	if f.beginErr != nil {
		return actions.ActivationTarget{}, f.beginErr
	}
	return actions.ActivationTarget{ServerID: "office", Address: "filees.example.net:22"}, nil
}
func (f *fakeActivator) Finish(_ context.Context, target actions.ActivationTarget, otp []byte) error {
	f.finishes <- target.ServerID + "|" + target.Address + "|" + string(otp)
	return f.finishErr
}
func (f *fakeActivator) Resume(_ context.Context, target actions.ActivationTarget) error {
	if f.resumes != nil {
		f.resumes <- target.ServerID + "|" + target.Address
	}
	return f.resumeErr
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

func TestControllerOpensCombinedJournalWithEmphasizedErrors(t *testing.T) {
	fake := &platformtest.Fake{}
	vm := app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRepoActivity: true, contract.CapErrorList: true},
		Repos:        []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Activity: []app.ActivityViewModel{
			{RepoID: "docs", Path: "a.txt", Stage: "published", Revision: 7, UpdatedAt: "2026-08-10T12:00:00Z"},
			{RepoID: "docs", Path: "b.txt", Stage: "published", Revision: 7, UpdatedAt: "2026-08-10T12:00:01Z"},
		},
		Errors: []app.ErrorViewModel{{ID: "err", RepoID: "docs", Timestamp: "2026-08-10T12:01:00Z", Severity: "ERROR", Code: "SVN-1", Message: "Odmowa"}},
	}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return vm }, JournalBrowser: fake, Notifier: fake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentJournal})
	deadline := time.Now().Add(time.Second)
	for len(fake.Snapshot().JournalRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := fake.Snapshot().JournalRequests
	if len(requests) != 1 || requests[0].Title != "Dziennik FileES" || len(requests[0].Rows) != 2 {
		t.Fatalf("journal requests=%+v", requests)
	}
	if !requests[0].Rows[0].Emphasized || !strings.Contains(requests[0].Rows[0].Summary, "⚠ BŁĄD") {
		t.Fatalf("error row=%+v", requests[0].Rows[0])
	}
	if requests[0].Rows[1].Summary != "Dokumenty — publikacja: 2 elementy · r7" || requests[0].Rows[1].Timestamp == "2026-08-10T12:00:01Z" {
		t.Fatalf("activity row=%+v", requests[0].Rows[1])
	}
}

func TestControllerRejectsAliasIntentForRealmWithRepositories(t *testing.T) {
	fake := &platformtest.Fake{}
	aliases := &fakeRealmAliases{aliases: make(chan string, 1)}
	vm := app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRealmAliasClaim: true}, Servers: []app.ServerViewModel{{
		ID: "manual", RealmID: "realm-acme", Repos: []app.RepoViewModel{{ID: "docs", OwnerRealmID: "realm-acme"}},
	}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return vm }, Prompter: fake, RealmAliases: aliases})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSetRealmAlias, ServerID: "manual"})
	time.Sleep(20 * time.Millisecond)
	if requests := fake.Snapshot().PromptRequests; len(requests) != 0 {
		t.Fatalf("alias prompt shown for established realm: %+v", requests)
	}
}

func TestControllerActivationPromptsForInvitationThenSecretOTP(t *testing.T) {
	responses := []platform.PromptTextResult{{Value: "filees-invite:v1:test"}, {Value: "OTP-CODE"}}
	fake := &platformtest.Fake{PromptTextFunc: func(_ context.Context, _ platform.PromptTextRequest) (platform.PromptTextResult, error) {
		result := responses[0]
		responses = responses[1:]
		return result, nil
	}}
	activator := &fakeActivator{begins: make(chan string, 1), finishes: make(chan string, 1)}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Opener: fake, Picker: fake, Prompter: fake, Notifier: fake, Locker: newFakeLocker(), Activator: activator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivate})
	if got := awaitCh(t, activator.begins, "activation begin"); got != "filees-invite:v1:test" {
		t.Fatalf("begin=%q", got)
	}
	if got := awaitCh(t, activator.finishes, "activation finish"); got != "office|filees.example.net:22|OTP-CODE" {
		t.Fatalf("finish=%q", got)
	}
	deadline := time.Now().Add(time.Second)
	for len(fake.Snapshot().PromptRequests) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := fake.Snapshot().PromptRequests
	if len(requests) != 2 || !requests[0].Secret || !requests[1].Secret {
		t.Fatalf("prompts=%#v", requests)
	}
}

func TestControllerResumesPendingActivationWithoutInvitationOrOTP(t *testing.T) {
	fake := &platformtest.Fake{
		ConfirmFunc: func(_ context.Context, _ platform.ConfirmRequest) (bool, error) { return true, nil },
		PromptTextFunc: func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
			t.Fatalf("unexpected prompt while reconnect resume succeeded: %+v", request)
			return platform.PromptTextResult{}, nil
		},
	}
	activator := &fakeActivator{
		begins: make(chan string, 1), finishes: make(chan string, 1), resumes: make(chan string, 1),
		pending: []actions.ActivationTarget{{ServerID: "spot", Address: "spot.example.net:2223"}},
	}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Prompter: fake, Notifier: fake, Activator: activator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivate})
	if got := awaitCh(t, activator.resumes, "activation resume"); got != "spot|spot.example.net:2223" {
		t.Fatalf("resume=%q", got)
	}
	select {
	case got := <-activator.begins:
		t.Fatalf("unexpected begin=%q", got)
	case got := <-activator.finishes:
		t.Fatalf("unexpected finish=%q", got)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestControllerPendingActivationFallsBackToStoredAttemptOTP(t *testing.T) {
	fake := &platformtest.Fake{
		ConfirmFunc: func(_ context.Context, _ platform.ConfirmRequest) (bool, error) { return true, nil },
		PromptTextFunc: func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
			if !request.Secret || !strings.Contains(request.Text, "OTP") {
				t.Fatalf("unexpected fallback prompt: %+v", request)
			}
			return platform.PromptTextResult{Value: "OTP-CODE"}, nil
		},
	}
	activator := &fakeActivator{
		begins: make(chan string, 1), finishes: make(chan string, 1), resumes: make(chan string, 1),
		pending: []actions.ActivationTarget{{ServerID: "spot", Address: "spot.example.net:2223"}}, resumeErr: errors.New("reconnect is not authorized yet"),
	}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Prompter: fake, Notifier: fake, Activator: activator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivate})
	awaitCh(t, activator.resumes, "activation resume")
	if got := awaitCh(t, activator.finishes, "activation OTP fallback"); got != "spot|spot.example.net:2223|OTP-CODE" {
		t.Fatalf("finish=%q", got)
	}
	select {
	case got := <-activator.begins:
		t.Fatalf("unexpected begin=%q", got)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestControllerActivationFailureUsesModalFallback(t *testing.T) {
	fake := &platformtest.Fake{PromptTextFunc: func(_ context.Context, _ platform.PromptTextRequest) (platform.PromptTextResult, error) {
		return platform.PromptTextResult{Value: "filees-invite:v1:test"}, nil
	}}
	activator := &fakeActivator{begins: make(chan string, 1), finishes: make(chan string, 1), beginErr: errors.New("bootstrap rejected")}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Prompter: fake, Notifier: fake, Activator: activator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivate})
	awaitCh(t, activator.begins, "activation begin")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := fake.Snapshot()
		if len(snapshot.InfoRequests) == 1 && len(snapshot.Notifications) == 1 {
			if snapshot.InfoRequests[0].Title != "Aktywacja FileES nie powiodła się" || snapshot.InfoRequests[0].Text != "bootstrap rejected" {
				t.Fatalf("modal fallback=%+v", snapshot.InfoRequests[0])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("activation failure was not visible: %+v", fake.Snapshot())
}

func TestControllerActivationStaysVisibleWhenAliasIsNotConfirmed(t *testing.T) {
	responses := []platform.PromptTextResult{{Value: "filees-invite:v1:test"}, {Value: "OTP-CODE"}, {Value: "acme"}}
	fake := &platformtest.Fake{
		PromptTextFunc: func(_ context.Context, _ platform.PromptTextRequest) (platform.PromptTextResult, error) {
			result := responses[0]
			responses = responses[1:]
			return result, nil
		},
		ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
			// Confirm the immutable value, then decline the retry dialog.
			return request.Title == "Potwierdź stały alias", nil
		},
	}
	activator := &fakeActivator{begins: make(chan string, 1), finishes: make(chan string, 1)}
	aliases := &fakeRealmAliases{errs: []error{errors.New("connection reset after server commit")}, aliases: make(chan string, 1)}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Opener: fake, Picker: fake, Prompter: fake, Notifier: fake, Locker: newFakeLocker(), Activator: activator, RealmAliases: aliases})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivate})
	awaitCh(t, activator.finishes, "activation finish")
	if got := awaitCh(t, aliases.aliases, "realm alias claim"); got != "acme" {
		t.Fatalf("alias=%q", got)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := fake.Snapshot()
		// A declined alias produces three notifications in a fixed order:
		// "Alias nie został ustawiony" from the claim, then the
		// "Klient aktywowany" notice, and only last the activation summary
		// this test asserts on. Waiting for a bare count (>= 2) was already
		// satisfied by the first two, so the assertion raced the summary and
		// failed about one run in six. Wait for the summary itself instead.
		var summary *platform.Notification
		for i := range snapshot.Notifications {
			if snapshot.Notifications[i].ID == "activation" {
				summary = &snapshot.Notifications[i]
			}
		}
		if len(snapshot.InfoRequests) > 0 && summary != nil {
			if !strings.Contains(snapshot.InfoRequests[0].Text, "jest aktywne") {
				t.Fatalf("info=%q", snapshot.InfoRequests[0].Text)
			}
			if !strings.Contains(summary.Body, "alias wymaga ustawienia") {
				t.Fatalf("activation summary=%q", summary.Body)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("activation completion was not communicated: %#v", fake.Snapshot())
}

func TestControllerOffersLocalPinSetupAfterActivationWhenNotConfigured(t *testing.T) {
	responses := []platform.PromptTextResult{{Value: "filees-invite:v1:test"}, {Value: "OTP-CODE"}, {Value: "4242"}}
	fake := &platformtest.Fake{PromptTextFunc: func(_ context.Context, _ platform.PromptTextRequest) (platform.PromptTextResult, error) {
		result := responses[0]
		responses = responses[1:]
		return result, nil
	}}
	pinStore, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	activator := &fakeActivator{begins: make(chan string, 1), finishes: make(chan string, 1)}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Opener: fake, Picker: fake, Prompter: fake, Notifier: fake, Locker: newFakeLocker(), Activator: activator, PinStore: pinStore})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivate})
	awaitCh(t, activator.finishes, "activation finish")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if configured, _ := pinStore.IsConfigured(); configured {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if configured, err := pinStore.IsConfigured(); err != nil || !configured {
		t.Fatalf("configured=%v err=%v, want PIN set up after activation", configured, err)
	}
	if ok, _, err := pinStore.Verify([]byte("4242")); err != nil || !ok {
		t.Fatalf("verify prompted PIN: ok=%v err=%v", ok, err)
	}
}

func TestControllerSkipsLocalPinPromptWhenAlreadyConfigured(t *testing.T) {
	responses := []platform.PromptTextResult{{Value: "filees-invite:v1:test"}, {Value: "OTP-CODE"}}
	fake := &platformtest.Fake{PromptTextFunc: func(_ context.Context, _ platform.PromptTextRequest) (platform.PromptTextResult, error) {
		if len(responses) == 0 {
			t.Error("unexpected extra prompt - PIN already configured")
			return platform.PromptTextResult{Cancelled: true}, nil
		}
		result := responses[0]
		responses = responses[1:]
		return result, nil
	}}
	pinStore, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pinStore.Setup([]byte("0000")); err != nil {
		t.Fatal(err)
	}
	activator := &fakeActivator{begins: make(chan string, 1), finishes: make(chan string, 1)}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Opener: fake, Picker: fake, Prompter: fake, Notifier: fake, Locker: newFakeLocker(), Activator: activator, PinStore: pinStore})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivate})
	awaitCh(t, activator.finishes, "activation finish")
	time.Sleep(50 * time.Millisecond)
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
	if len(opened) != 1 || opened[0] != wcPath("/wc/repo1") {
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
	gated := newGatedPicker(platform.PickFilesResult{Paths: []string{wcPath("/wc/repo1/file.dwg")}})
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
	gated := newGatedPicker(platform.PickFilesResult{Paths: []string{wcPath("/wc/repo1/file.dwg")}})
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
			return platform.PickFilesResult{Paths: []string{wcPath("/wc/repo-other/file.dwg")}}, nil
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
	refreshCh := make(chan struct{}, 1)
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
		Refresh:   func() { refreshCh <- struct{}{} },
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentLock, RepoID: "repo1"})
	awaitCh(t, locker.lockCh, "Lock call")
	n := awaitCh(t, notifCh, "success notification")
	if n.Urgency != platform.UrgencyLow {
		t.Fatalf("success notification urgency = %v", n.Urgency)
	}
	if n.Body != "/a.dwg\n/b.dwg" {
		t.Fatalf("success notification body = %q, want relative locked paths", n.Body)
	}
	awaitCh(t, refreshCh, "post-lock refresh")
}

func TestControllerReservationsConfirmsRiskAndTokenFencesRelease(t *testing.T) {
	reservation := app.Reservation{RepoID: "docs", WorkingCopy: "/wc/docs", Path: "plan.dwg", Token: "opaque-lock-token", OwnerLabel: "anna", CreatedAt: "2026-07-27", CanRelease: true, LocalChanges: true}
	manager := newFakeReservations([]app.Reservation{reservation})
	shows := 0
	fake := &platformtest.Fake{
		ReservationsFunc: func(_ context.Context, request platform.ReservationDialogRequest) (platform.ReservationDialogResult, error) {
			shows++
			if shows == 1 {
				if len(request.Rows) != 1 || request.Rows[0].ID == reservation.Token || request.Rows[0].Action != "Zwolnij" {
					t.Fatalf("dialog rows=%+v", request.Rows)
				}
				return platform.ReservationDialogResult{Action: platform.ReservationDialogRelease, RowID: request.Rows[0].ID}, nil
			}
			return platform.ReservationDialogResult{Action: platform.ReservationDialogClose}, nil
		},
		ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
			if !strings.Contains(request.Text, "lokalne zmiany") || !strings.Contains(request.Text, "nie bada uchwytów") {
				t.Fatalf("risk confirmation text=%q", request.Text)
			}
			return true, nil
		},
	}
	vm := &vmStore{}
	vm.Store(app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoReservationList: true, contract.CapRepoReservationRelease: true}, Servers: []app.ServerViewModel{{ID: "office", ReservationsKnown: true, ReservationCount: 1}}})
	intents, cancel := setup(actions.Config{ViewModel: vm.Load, Prompter: fake, Notifier: fake, Reservations: manager, ReservationBrowser: fake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentReservations})
	if got := awaitCh(t, manager.listCh, "reservation list"); got != "office" {
		t.Fatalf("server ID=%q", got)
	}
	call := awaitCh(t, manager.release, "reservation release")
	if call.payload.ServerID != "office" || call.payload.RepoID != "docs" || call.payload.Path != "plan.dwg" || call.payload.ExpectedToken != reservation.Token || !call.payload.ConfirmRisk {
		t.Fatalf("release payload=%+v", call.payload)
	}
}

func TestControllerInlineReservationReleaseResolvesOpaqueIDAndRefreshes(t *testing.T) {
	reservation := app.Reservation{
		ID: "safe-row-id", ServerID: "office", RepoID: "docs", WorkingCopy: "/wc/docs",
		Path: "plan.dwg", Token: "opaque-lock-token", CanRelease: true, ActivePassport: true,
	}
	manager := newFakeReservations(nil)
	refreshCh := make(chan struct{}, 1)
	fake := &platformtest.Fake{ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
		if request.Title != "Zwolnij rezerwację" || !strings.Contains(request.Text, "aktywny paszport") {
			t.Fatalf("confirmation=%+v", request)
		}
		return true, nil
	}}
	vm := &vmStore{}
	vm.Store(app.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoReservationList: true, contract.CapRepoReservationRelease: true,
		},
		Servers:      []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}}}},
		Reservations: []app.Reservation{reservation},
	})
	intents, cancel := setup(actions.Config{
		ViewModel: vm.Load, Prompter: fake, Notifier: fake, Reservations: manager,
		Refresh: func() { refreshCh <- struct{}{} },
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentReleaseReservation, ReservationID: reservation.ID})
	call := awaitCh(t, manager.release, "inline reservation release")
	if call.payload.ServerID != "office" || call.payload.RepoID != "docs" || call.payload.Path != "plan.dwg" || call.payload.ExpectedToken != reservation.Token || !call.payload.ConfirmRisk {
		t.Fatalf("release payload=%+v", call.payload)
	}
	awaitCh(t, refreshCh, "post-release refresh")
}

func TestControllerReservationsReleaseAllSkipsForeignLocks(t *testing.T) {
	first := app.Reservation{RepoID: "docs", WorkingCopy: "/wc/docs", Path: "own-a.dwg", Token: "token-a", CanRelease: true}
	foreign := app.Reservation{RepoID: "docs", WorkingCopy: "/wc/docs", Path: "foreign.dwg", Token: "token-foreign", CanRelease: false}
	second := app.Reservation{RepoID: "docs", WorkingCopy: "/wc/docs", Path: "own-b.dwg", Token: "token-b", CanRelease: true, ActivePassport: true}
	manager := newFakeReservations([]app.Reservation{first, foreign, second})
	refreshCh := make(chan struct{}, 1)
	shows := 0
	fake := &platformtest.Fake{
		ReservationsFunc: func(_ context.Context, request platform.ReservationDialogRequest) (platform.ReservationDialogResult, error) {
			shows++
			if len(request.Rows) != 3 || request.Rows[1].Action != "Poproś o zwolnienie (wkrótce)" {
				t.Fatalf("dialog rows=%+v", request.Rows)
			}
			if shows > 1 {
				return platform.ReservationDialogResult{Action: platform.ReservationDialogClose}, nil
			}
			return platform.ReservationDialogResult{Action: platform.ReservationDialogReleaseAll}, nil
		},
		ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
			if request.Title != "Zwolnij wszystkie moje rezerwacje" || !strings.Contains(request.Text, "(2)") || !strings.Contains(request.Text, "Cudze blokady") {
				t.Fatalf("confirmation=%+v", request)
			}
			return true, nil
		},
	}
	vm := &vmStore{}
	vm.Store(app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoReservationList: true, contract.CapRepoReservationRelease: true}, Servers: []app.ServerViewModel{{ID: "office", ReservationsKnown: true, ReservationCount: 3}}})
	intents, cancel := setup(actions.Config{ViewModel: vm.Load, Prompter: fake, Notifier: fake, Reservations: manager, ReservationBrowser: fake, Refresh: func() { refreshCh <- struct{}{} }})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentReservations})
	firstCall := awaitCh(t, manager.release, "first batch release")
	secondCall := awaitCh(t, manager.release, "second batch release")
	if firstCall.payload.ExpectedToken != first.Token || secondCall.payload.ExpectedToken != second.Token || !secondCall.payload.ConfirmRisk {
		t.Fatalf("batch payloads=%+v / %+v", firstCall.payload, secondCall.payload)
	}
	awaitCh(t, refreshCh, "post-batch refresh")
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
	gated1 := newGatedPicker(platform.PickFilesResult{Paths: []string{wcPath("/wc/r1/a.dwg")}})
	gated2 := newGatedPicker(platform.PickFilesResult{Paths: []string{wcPath("/wc/r2/b.dwg")}})
	fake := &platformtest.Fake{
		PickFilesFunc: func(ctx context.Context, req platform.PickFilesRequest) (platform.PickFilesResult, error) {
			if req.Root == wcPath("/wc/r1") {
				return gated1.PickFiles(ctx, req)
			}
			if req.Root == wcPath("/wc/r2") {
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
	gated := newGatedPicker(platform.PickFilesResult{Paths: []string{wcPath("/wc/r1/a.dwg")}})
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

type fakeUpdater struct {
	planCalls  chan struct{}
	applyCalls chan struct{}
}

func (fake *fakeUpdater) UpdatePlan(context.Context) (*actions.UpdatePlan, error) {
	fake.planCalls <- struct{}{}
	return &actions.UpdatePlan{
		CurrentVersion: "1.0", AvailableVersion: "1.1", ReleaseID: "r180", RestartRequired: true,
		Changes: []actions.UpdateChange{{Action: "update", Path: "filees-gui", Detail: "podpis zweryfikowany"}},
	}, nil
}

func (fake *fakeUpdater) UpdateApply(context.Context) (*actions.UpdateResult, error) {
	fake.applyCalls <- struct{}{}
	return &actions.UpdateResult{InstalledVersion: "1.1", RestartRequired: true}, nil
}

func TestControllerUpdateDryRunAndConfirmedApply(t *testing.T) {
	updater := &fakeUpdater{planCalls: make(chan struct{}, 2), applyCalls: make(chan struct{}, 1)}
	platformFake := &platformtest.Fake{ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil }}
	restarted := make(chan struct{}, 1)
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return app.ViewModel{} }, Prompter: platformFake,
		Notifier: platformFake, Updater: updater, Restart: func() { restarted <- struct{}{} },
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentUpdatePlan})
	awaitCh(t, updater.planCalls, "dry-run plan")
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	info := platformFake.Snapshot().InfoRequests
	if len(info) != 1 || !strings.Contains(info[0].Text, "UPDATE  filees-gui") {
		t.Fatalf("dry-run dialog = %#v", info)
	}

	send(t, intents, tray.Intent{Kind: tray.IntentUpdateApply})
	awaitCh(t, updater.planCalls, "fresh apply plan")
	awaitCh(t, updater.applyCalls, "confirmed apply")
	awaitCh(t, restarted, "restart callback")
	confirm := platformFake.Snapshot().ConfirmRequests
	if len(confirm) != 1 || confirm[0].ConfirmText != "Zaktualizuj" || !strings.Contains(confirm[0].Text, "podpis zweryfikowany") {
		t.Fatalf("confirmation = %#v", confirm)
	}
}

func TestControllerServerInformationContainsPermissions(t *testing.T) {
	platformFake := &platformtest.Fake{}
	view := app.ViewModel{Servers: []app.ServerViewModel{{
		ID: "office", Address: "filees.example", SSHPort: 22, ClientID: "client-1",
		ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true,
	}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, Prompter: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentServerInfo, ServerID: "office"})
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := platformFake.Snapshot().InfoRequests
	if len(requests) != 1 || !strings.Contains(requests[0].Text, "Tryb klienta: pełny") || !strings.Contains(requests[0].Text, "Tworzenie repozytoriów: dozwolone") {
		t.Fatalf("server information = %#v", requests)
	}
}

func TestControllerShowsSettingsOverviewForServersAndFolders(t *testing.T) {
	platformFake := &platformtest.Fake{}
	view := app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoAttachIntent: true, contract.CapRepoAttachApprove: true}, Servers: []app.ServerViewModel{{
		ID: "office", DisplayName: "Biuro", Address: "filees.example", SSHPort: 2222,
		RealmID: "realm-1", RealmAlias: "acme", ClientID: "client-1",
		Repos: []app.RepoViewModel{
			{ID: "docs", DisplayName: "Dokumenty", Attached: true, LocalPath: "/wc/docs", Access: contract.AccessReadWrite, State: contract.StateActive},
			{ID: "remote", DisplayName: "Zdalna projekcja", AttachmentPolicy: "optional", State: contract.StateUnattached},
			{ID: "import", DisplayName: "Biblia Audio KIDS", LocalPath: "/wc/biblia", Access: contract.AccessReadWrite, AttachmentPolicy: "optional", State: contract.StateInitializing},
		},
	}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, SettingsBrowser: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().SettingsRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := platformFake.Snapshot().SettingsRequests
	if len(requests) != 1 || requests[0].Title != "FileES — Biuro" || len(requests[0].Servers) != 1 {
		t.Fatalf("settings request = %#v", requests)
	}
	server := requests[0].Servers[0]
	if server.Name != "Biuro" || server.Address != "filees.example" || !strings.Contains(server.Realm, "acme") || len(server.Folders) != 3 {
		t.Fatalf("settings server = %#v", server)
	}
	folder := server.Folders[0]
	if folder.Name != "Dokumenty" || folder.LocalPath != "/wc/docs" || folder.State != "aktywne" || folder.Access != "odczyt i zapis" {
		t.Fatalf("settings folder = %#v", folder)
	}
	remote := server.Folders[1]
	if remote.ID != "remote" || remote.LocalPath != "brak lokalnego folderu" || !remote.CanConnect {
		t.Fatalf("unattached repository = %#v", remote)
	}
	pending := server.Folders[2]
	if pending.ID != "import" || pending.LocalPath != "/wc/biblia" || pending.State != "import początkowy w toku" || pending.CanConnect {
		t.Fatalf("pending repository creation = %#v", pending)
	}
}

func TestControllerSettingsFromFileESMenuListsEveryServer(t *testing.T) {
	platformFake := &platformtest.Fake{}
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{
		{ID: "office", DisplayName: "Biuro"},
		{ID: "lab", DisplayName: "Laboratorium"},
	}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, SettingsBrowser: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings})
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().SettingsRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := platformFake.Snapshot().SettingsRequests
	if len(requests) != 1 || requests[0].Title != "Ustawienia FileES" || len(requests[0].Servers) != 2 {
		t.Fatalf("settings request = %#v", requests)
	}
	if requests[0].Servers[0].ID != "office" || requests[0].Servers[1].ID != "lab" {
		t.Fatalf("servers = %#v", requests[0].Servers)
	}
}

func TestControllerRepositorySettingsFocusesOneValidatedFolder(t *testing.T) {
	platformFake := &platformtest.Fake{}
	view := lifecycleView(contract.CapRepoPublicShareList, contract.CapRepoPublicShareCreate, contract.CapRepoPublicShareUpdate, contract.CapRepoPublicShareRevoke, contract.CapRepoPublicShareDelete, contract.CapRepoDetach, contract.CapRepoDelete)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office", RepoID: "repo-1"})
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().SettingsRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := platformFake.Snapshot().SettingsRequests
	if len(requests) != 1 || requests[0].FocusRepoID != "repo-1" || len(requests[0].Servers) != 1 || len(requests[0].Servers[0].Folders) != 1 || requests[0].Servers[0].Folders[0].ID != "repo-1" {
		t.Fatalf("focused repository settings = %#v", requests)
	}
}

func TestControllerConnectsSelectedRealmRepositoriesToFoldersSequentially(t *testing.T) {
	attacher := &fakeRepositoryAttacher{calls: make(chan attachCall, 2)}
	paths := []string{filepath.Join(t.TempDir(), "docs"), filepath.Join(t.TempDir(), "cad")}
	var pickerIndex int
	var settingsCalls int
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			settingsCalls++
			if settingsCalls == 1 {
				return platform.SettingsDialogResult{Action: platform.SettingsDialogConnectRepos, ServerID: "office", RepoIDs: []string{"docs", "cad"}}, nil
			}
			return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
		},
		PickFolderFunc: func(_ context.Context, request platform.PickFolderRequest) (platform.PickFolderResult, error) {
			if pickerIndex >= len(paths) {
				t.Fatalf("unexpected folder picker: %#v", request)
			}
			path := paths[pickerIndex]
			pickerIndex++
			return platform.PickFolderResult{Path: path}, nil
		},
	}
	view := app.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoAttachIntent:  true,
			contract.CapRepoAttachApprove: true,
		},
		Servers: []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{
			{ID: "docs", DisplayName: "Dokumenty", State: contract.StateUnattached},
			{ID: "cad", DisplayName: "CAD", State: contract.StateUnattached},
		}}},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, SettingsBrowser: platformFake, FolderPicker: platformFake,
		RepositoryAttacher: attacher, Notifier: platformFake, CreationStatusPollInterval: time.Millisecond,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	for index, repoID := range []string{"docs", "cad"} {
		select {
		case call := <-attacher.calls:
			if call.serverID != "office" || call.repoID != repoID || call.localPath != paths[index] {
				t.Fatalf("attach %d = %#v", index, call)
			}
		case <-time.After(time.Second):
			t.Fatalf("attachment %s was not started", repoID)
		}
	}
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().SettingsRequests) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(platformFake.Snapshot().SettingsRequests); got != 2 {
		t.Fatalf("settings windows = %d, want reopened after folder selection", got)
	}
	reopened := platformFake.Snapshot().SettingsRequests[1]
	if !strings.Contains(reopened.Text, "Pierwszy checkout trwa w tle") || len(reopened.Servers) != 1 || len(reopened.Servers[0].Folders) != 2 {
		t.Fatalf("reopened settings do not explain pending checkout: %#v", reopened)
	}
	for index, folder := range reopened.Servers[0].Folders {
		if folder.State != "łączenie…" || folder.LocalPath != paths[index] || folder.CanConnect {
			t.Errorf("pending folder %d = %#v, want path %q and non-connectable łączenie state", index, folder, paths[index])
		}
	}

	// Lifecycle success alone keeps the overlay until the authoritative model
	// catches up. Once it does, the next Settings snapshot must drop the
	// optimistic label and use normal attached state.
	for index := range view.Servers[0].Repos {
		view.Servers[0].Repos[index].Attached = true
		view.Servers[0].Repos[index].LocalPath = paths[index]
		view.Servers[0].Repos[index].State = contract.StateActive
	}
	time.Sleep(10 * time.Millisecond) // let the auto-reopened dialog release its operation key
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline = time.Now().Add(time.Second)
	for len(platformFake.Snapshot().SettingsRequests) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := platformFake.Snapshot().SettingsRequests
	if len(requests) != 3 {
		t.Fatalf("settings windows = %d, want explicit post-tick reopen", len(requests))
	}
	for index, folder := range requests[2].Servers[0].Folders {
		if folder.State != "aktywne" || folder.LocalPath != paths[index] {
			t.Errorf("post-tick folder %d = %#v, want authoritative active path %q", index, folder, paths[index])
		}
	}
}

func TestControllerShowsModalWhenRepositoryAttachmentIsRejected(t *testing.T) {
	attacher := &fakeRepositoryAttacher{calls: make(chan attachCall, 1), err: fakeStructuredAttachError{}}
	target := filepath.Join(t.TempDir(), "janczewice")
	settingsCalls := 0
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			settingsCalls++
			if settingsCalls == 1 {
				return platform.SettingsDialogResult{Action: platform.SettingsDialogConnectRepos, ServerID: "office", RepoIDs: []string{"repo-1"}}, nil
			}
			return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
		},
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: target}, nil
		},
	}
	view := app.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoAttachIntent:  true,
			contract.CapRepoAttachApprove: true,
		},
		Servers: []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{{
			ID: "repo-1", DisplayName: "JANCZEWICE", State: contract.StateUnattached,
		}}}},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, SettingsBrowser: platformFake,
		FolderPicker: platformFake, Prompter: platformFake, RepositoryAttacher: attacher, Notifier: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case call := <-attacher.calls:
		if call.repoID != "repo-1" || call.localPath != target {
			t.Fatalf("attach call=%#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("attachment was not attempted")
	}
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := platformFake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || snapshot.InfoRequests[0].Title != "Nie można połączyć repozytorium" || !strings.Contains(snapshot.InfoRequests[0].Text, "lokalny stan repozytorium") || strings.Contains(snapshot.InfoRequests[0].Text, "wire fallback") {
		t.Fatalf("modal errors=%#v", snapshot.InfoRequests)
	}
	if len(snapshot.Notifications) != 1 || snapshot.Notifications[0].Urgency != platform.UrgencyCritical {
		t.Fatalf("notifications=%#v", snapshot.Notifications)
	}
}

func TestControllerOffersAndLocatesMovedWorkingCopy(t *testing.T) {
	locator := &fakeRepositoryLocator{calls: make(chan locateCall, 1)}
	target := filepath.Join(t.TempDir(), "ZEGRZE")
	operation := "working_copy_missing"
	var request platform.SettingsDialogRequest
	platformFake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, got platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			request = got
			return platform.SettingsDialogResult{Action: platform.SettingsDialogLocateFolder, ServerID: "office", RepoID: "repo-1"}, nil
		},
		PickFolderFunc: func(_ context.Context, got platform.PickFolderRequest) (platform.PickFolderResult, error) {
			if !strings.Contains(got.Title, "przeniesioną kopię roboczą") {
				t.Fatalf("picker title=%q", got.Title)
			}
			return platform.PickFolderResult{Path: target}, nil
		},
	}
	view := app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRepoLocate: true},
		Servers: []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{{
			ID: "repo-1", DisplayName: "ZEGRZE", Attached: true, LocalPath: filepath.Join(t.TempDir(), "missing"),
			State: contract.StateInteractionRequired, CurrentOp: &operation,
		}}}},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, SettingsBrowser: platformFake,
		FolderPicker: platformFake, Prompter: platformFake, RepositoryLocator: locator, Notifier: platformFake,
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: time.Second,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case call := <-locator.calls:
		if call != (locateCall{serverID: "office", repoID: "repo-1", localPath: target}) {
			t.Fatalf("locate=%#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("locate was not started")
	}
	if len(request.Servers) != 1 || len(request.Servers[0].Folders) != 1 || !request.Servers[0].Folders[0].CanLocate {
		t.Fatalf("missing working-copy row=%#v", request)
	}
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if infos := platformFake.Snapshot().InfoRequests; len(infos) != 1 || infos[0].Title != "Kopia robocza została wskazana" {
		t.Fatalf("success modal=%#v", infos)
	}
}

func TestControllerReportsImmediateLocateRejectionAsModal(t *testing.T) {
	locator := &fakeRepositoryLocator{calls: make(chan locateCall, 1), err: fakeStructuredLocateError{}}
	target := filepath.Join(t.TempDir(), "plain")
	platformFake := &platformtest.Fake{
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: target}, nil
		},
	}
	operation := "working_copy_missing"
	view := app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRepoLocate: true},
		Servers: []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{{
			ID: "repo-1", DisplayName: "ZEGRZE", Attached: true, LocalPath: filepath.Join(t.TempDir(), "missing"),
			State: contract.StateInteractionRequired, CurrentOp: &operation,
		}}}},
	}
	intents, cancel := setup(actions.Config{
		ViewModel:    func() app.ViewModel { return view },
		FolderPicker: platformFake, Prompter: platformFake, RepositoryLocator: locator, Notifier: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentLocateFolder, ServerID: "office", RepoID: "repo-1"})
	awaitCh(t, locator.calls, "locate")
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := platformFake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || snapshot.InfoRequests[0].Title != "Nie można połączyć przeniesionej kopii" || !strings.Contains(snapshot.InfoRequests[0].Text, "przeniesionej kopii") || strings.Contains(snapshot.InfoRequests[0].Text, "wire") {
		t.Fatalf("immediate locate modal=%#v", snapshot.InfoRequests)
	}
}

func TestControllerReportsWrongWorkingCopyLocateAsModal(t *testing.T) {
	locator := &fakeRepositoryLocator{calls: make(chan locateCall, 1), lastError: "relocated working copy URL does not match projected repository"}
	target := filepath.Join(t.TempDir(), "WRONG")
	platformFake := &platformtest.Fake{
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: target}, nil
		},
	}
	operation := "working_copy_missing"
	view := app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRepoLocate: true},
		Servers: []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{{
			ID: "repo-1", DisplayName: "ZEGRZE", Attached: true, LocalPath: filepath.Join(t.TempDir(), "missing"),
			State: contract.StateInteractionRequired, CurrentOp: &operation,
		}}}},
	}
	intents, cancel := setup(actions.Config{
		ViewModel:    func() app.ViewModel { return view },
		FolderPicker: platformFake, Prompter: platformFake, RepositoryLocator: locator, Notifier: platformFake,
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: time.Second,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentLocateFolder, ServerID: "office", RepoID: "repo-1"})
	awaitCh(t, locator.calls, "locate")
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := platformFake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || snapshot.InfoRequests[0].Title != "Nie można połączyć przeniesionej kopii" || !strings.Contains(snapshot.InfoRequests[0].Text, "innego repozytorium") || strings.Contains(snapshot.InfoRequests[0].Text, "does not match") {
		t.Fatalf("locate failure modal=%#v", snapshot.InfoRequests)
	}
	if len(snapshot.Notifications) != 1 || snapshot.Notifications[0].Urgency != platform.UrgencyCritical {
		t.Fatalf("notifications=%#v", snapshot.Notifications)
	}
}

func TestControllerSettingsOpensRecoveriesWhenNoServersRemain(t *testing.T) {
	var got platform.SettingsDialogRequest
	fake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			got = request
			return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
		},
	}
	view := app.ViewModel{Connected: true, Recoveries: []app.RecoveryViewModel{{
		OperationID: "op-1", ServerName: "spot", CanDownload: true, DownloadUntil: "2026-09-01T00:00:00Z",
	}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, SettingsBrowser: fake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings})
	deadline := time.Now().Add(time.Second)
	for len(got.Recoveries) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(got.Servers) != 0 || len(got.Recoveries) != 1 || got.Recoveries[0].OperationID != "op-1" || !got.Recoveries[0].CanDownload {
		t.Fatalf("settings=%#v", got)
	}
}

type pairMobileCall struct{ serverID string }
type fakeMobilePairer struct {
	calls chan pairMobileCall
	err   error
}

func (f *fakeMobilePairer) Launch(_ context.Context, serverID string) error {
	f.calls <- pairMobileCall{serverID}
	return f.err
}

func TestControllerLaunchesMobilePairingHelper(t *testing.T) {
	pairer := &fakeMobilePairer{calls: make(chan pairMobileCall, 1)}
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, MobilePairer: pairer})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentPairMobileDevice, ServerID: "office"})
	select {
	case call := <-pairer.calls:
		if call.serverID != "office" {
			t.Fatalf("pair call = %#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("mobile pairing was not launched")
	}
}

func TestControllerSurfacesMobilePairingLaunchFailure(t *testing.T) {
	pairer := &fakeMobilePairer{calls: make(chan pairMobileCall, 1), err: errors.New("helper binary not found")}
	fake := &platformtest.Fake{}
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, MobilePairer: pairer, Notifier: fake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentPairMobileDevice, ServerID: "office"})
	<-pairer.calls

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, n := range fake.Snapshot().Notifications {
			if n.Title == "Nie można sparować urządzenia mobilnego" {
				if !strings.Contains(n.Body, "helper binary not found") || n.Urgency != platform.UrgencyCritical {
					t.Fatalf("failure notification = %#v", n)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("mobile pairing launch failure was never surfaced")
}

func TestControllerDoesNotLaunchMobilePairingWhileDisconnectedOrStale(t *testing.T) {
	pairer := &fakeMobilePairer{calls: make(chan pairMobileCall, 1)}
	view := app.ViewModel{Connected: true, Stale: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, MobilePairer: pairer})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentPairMobileDevice, ServerID: "office"})
	select {
	case call := <-pairer.calls:
		t.Fatalf("mobile pairing launched while stale: %#v", call)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestControllerCreatesRepositoryAfterNativeFolderAndConfirmation(t *testing.T) {
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	fake := &platformtest.Fake{
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: "/data/projekt"}, nil
		},
		PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
			return platform.PromptTextResult{Value: "Projekt A"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake, RepositoryCreator: creator, Notifier: fake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	select {
	case call := <-creator.calls:
		if call.serverID != "office" || call.displayName != "Projekt A" || call.localPath != "/data/projekt" {
			t.Fatalf("create call = %#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("repository creation was not requested")
	}
	snapshot := fake.Snapshot()
	if len(snapshot.ConfirmRequests) != 1 || !strings.Contains(snapshot.ConfirmRequests[0].Text, "Dostęp: odczyt i zapis") {
		t.Fatalf("confirmation = %#v", snapshot.ConfirmRequests)
	}
}

func TestControllerSurfacesAsyncRepositoryCreationFailure(t *testing.T) {
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	creator.statusFunc = func(string) (string, string, error) {
		return "error", "STORAGE_INSUFFICIENT: server storage requires 187424908 bytes, 118276096 available", nil
	}
	fake := &platformtest.Fake{
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: "/data/skany"}, nil
		},
		PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
			return platform.PromptTextResult{Value: "Skany"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake,
		RepositoryCreator: creator, Notifier: fake,
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: time.Second,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	<-creator.calls

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, n := range fake.Snapshot().Notifications {
			if n.Title == "Nie udało się utworzyć repozytorium" {
				if !strings.Contains(n.Body, "STORAGE_INSUFFICIENT") {
					t.Fatalf("failure notification body = %q, want it to contain the real error", n.Body)
				}
				if n.Urgency != platform.UrgencyCritical {
					t.Fatalf("failure notification urgency = %v, want Critical", n.Urgency)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("async repository creation failure was never surfaced to the user")
}

func TestControllerRetriesTransientCreationStatusFailure(t *testing.T) {
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	var statusCalls int
	creator.statusFunc = func(string) (string, string, error) {
		statusCalls++
		if statusCalls == 1 {
			return "", "", errors.New("daemon restarting")
		}
		return "attached", "", nil
	}
	fake := repositoryCreationPlatform()
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake,
		RepositoryCreator: creator, Notifier: fake,
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: 100 * time.Millisecond,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	<-creator.calls
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, notification := range fake.Snapshot().Notifications {
			if notification.Title == "Repozytorium utworzone" {
				if statusCalls < 2 {
					t.Fatalf("status calls=%d", statusCalls)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("transient status error stopped monitoring; calls=%d notifications=%+v", statusCalls, fake.Snapshot().Notifications)
}

func TestControllerReportsUnknownCreationStatusAfterTimeout(t *testing.T) {
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	creator.statusFunc = func(string) (string, string, error) {
		return "", "", errors.New("IPC unavailable")
	}
	fake := repositoryCreationPlatform()
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake,
		RepositoryCreator: creator, Notifier: fake,
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: 10 * time.Millisecond,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	<-creator.calls
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, notification := range fake.Snapshot().Notifications {
			if notification.Title == "Status tworzenia repozytorium jest nieznany" {
				if !strings.Contains(notification.Body, "IPC unavailable") || !strings.Contains(notification.Body, "op-123") {
					t.Fatalf("unknown status body=%q", notification.Body)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing unknown-status notification: %+v", fake.Snapshot().Notifications)
}

func TestControllerTimeoutCancelsBlockedCreationStatus(t *testing.T) {
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	creator.statusContextFunc = func(ctx context.Context, _ string) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	fake := repositoryCreationPlatform()
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake,
		RepositoryCreator: creator, Notifier: fake,
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: 10 * time.Millisecond,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	<-creator.calls
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, notification := range fake.Snapshot().Notifications {
			if notification.Title == "Status tworzenia repozytorium jest nieznany" {
				if !strings.Contains(notification.Body, "context deadline exceeded") {
					t.Fatalf("blocked status body=%q", notification.Body)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("blocked status call outlived monitor deadline: %+v", fake.Snapshot().Notifications)
}

func TestControllerSurfacesRecoverableInitialImportFailure(t *testing.T) {
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	creator.statusFunc = func(string) (string, string, error) {
		return "repository_created", "INITIAL_IMPORT_FAILED: disk full", nil
	}
	fake := repositoryCreationPlatform()
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake,
		RepositoryCreator: creator, Notifier: fake,
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: time.Second,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	<-creator.calls
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, notification := range fake.Snapshot().Notifications {
			if notification.Title == "Nie udało się dokończyć tworzenia repozytorium" {
				if !strings.Contains(notification.Body, "Ponowienie użyje już utworzonego repozytorium") {
					t.Fatalf("recoverable failure body=%q", notification.Body)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("recoverable failure was not surfaced: %+v", fake.Snapshot().Notifications)
}

func repositoryCreationPlatform() *platformtest.Fake {
	return &platformtest.Fake{
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: "/data/projekt"}, nil
		},
		PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
			return platform.PromptTextResult{Value: "Projekt"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
}

func TestControllerDoesNotCreateRepositoryWhenAuthorityIsStale(t *testing.T) {
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	fake := &platformtest.Fake{PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
		return platform.PickFolderResult{Path: "/data/projekt"}, nil
	}}
	view := app.ViewModel{Connected: true, Stale: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake, RepositoryCreator: creator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	time.Sleep(20 * time.Millisecond)
	if len(fake.Snapshot().FolderRequests) != 0 {
		t.Fatal("folder picker opened for stale authority")
	}
	select {
	case <-creator.calls:
		t.Fatal("create called for stale authority")
	default:
	}
}
