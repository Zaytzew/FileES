package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

type fakeTrayBackend struct {
	ready chan struct{}
	quit  chan struct{}
	once  sync.Once

	mu     sync.Mutex
	resets int
	items  map[string]*fakeTrayItem
	title  string
}

type repositoryAttachClientStub struct {
	intents   []contract.RepoAttachIntentPayload
	approvals []contract.RepoAttachApprovePayload
}

type repositoryLocateClientStub struct {
	payloads []contract.RepoLocatePayload
}

func (stub *repositoryLocateClientStub) RepoLocate(_ context.Context, payload contract.RepoLocatePayload) (*contract.RepoLifecycleResult, error) {
	stub.payloads = append(stub.payloads, payload)
	return &contract.RepoLifecycleResult{OperationID: "op-locate", State: "relocating"}, nil
}

func (stub *repositoryAttachClientStub) RepoAttachIntent(_ context.Context, payload contract.RepoAttachIntentPayload) (*contract.RepoLifecycleResult, error) {
	stub.intents = append(stub.intents, payload)
	return &contract.RepoLifecycleResult{OperationID: "op-attach", State: "unattached"}, nil
}

func (stub *repositoryAttachClientStub) RepoAttachApprove(_ context.Context, payload contract.RepoAttachApprovePayload) (*contract.RepoLifecycleResult, error) {
	stub.approvals = append(stub.approvals, payload)
	return &contract.RepoLifecycleResult{OperationID: payload.OperationID, State: "attaching"}, nil
}

func (stub *repositoryAttachClientStub) RepoLifecycleStatus(_ context.Context, operationID string) (*contract.RepoLifecycleResult, error) {
	return &contract.RepoLifecycleResult{OperationID: operationID, State: "attached"}, nil
}

func TestRepositoryAttachAdapterPersistsIntentBeforeApproval(t *testing.T) {
	stub := &repositoryAttachClientStub{}
	adapter := repositoryAttachAdapter{client: stub}
	path := filepath.Join(t.TempDir(), "repo")
	operationID, err := adapter.AttachRepository(context.Background(), "office", "repo-1", path)
	if err != nil {
		t.Fatalf("AttachRepository: %v", err)
	}
	if operationID != "op-attach" || len(stub.intents) != 1 || len(stub.approvals) != 1 {
		t.Fatalf("operation=%q intents=%#v approvals=%#v", operationID, stub.intents, stub.approvals)
	}
	if stub.intents[0].ServerID != "office" || stub.intents[0].RepoID != "repo-1" || stub.intents[0].LocalPath != path {
		t.Fatalf("intent=%#v", stub.intents[0])
	}
	if stub.approvals[0].OperationID != operationID || stub.approvals[0].ServerID != "office" || stub.approvals[0].RepoID != "repo-1" {
		t.Fatalf("approval=%#v", stub.approvals[0])
	}
}

func TestRepositoryLocateAdapterUsesExistingWorkingCopyCommand(t *testing.T) {
	stub := &repositoryLocateClientStub{}
	path := filepath.Join(t.TempDir(), "moved")
	operationID, err := (repositoryLocateAdapter{client: stub}).LocateRepository(context.Background(), "office", "repo-1", path)
	if err != nil || operationID != "op-locate" {
		t.Fatalf("operation=%q err=%v", operationID, err)
	}
	if len(stub.payloads) != 1 || stub.payloads[0] != (contract.RepoLocatePayload{ServerID: "office", RepoID: "repo-1", ExistingLocalPath: path}) {
		t.Fatalf("payloads=%#v", stub.payloads)
	}
}

func newFakeTrayBackend() *fakeTrayBackend {
	return &fakeTrayBackend{
		ready: make(chan struct{}),
		quit:  make(chan struct{}),
		items: make(map[string]*fakeTrayItem),
	}
}

func (b *fakeTrayBackend) Run(onReady, onExit func()) {
	onReady()
	close(b.ready)
	<-b.quit
	onExit()
}
func (b *fakeTrayBackend) Quit() { b.once.Do(func() { close(b.quit) }) }
func (b *fakeTrayBackend) ResetMenu() {
	b.mu.Lock()
	b.resets++
	b.items = make(map[string]*fakeTrayItem)
	b.mu.Unlock()
}
func (*fakeTrayBackend) SetIcon([]byte) {}

// SetTitle is recorded because the connection status lives in the tray header
// and nowhere else. Until r421 the header text was also mirrored into a
// disabled "system.status" menu row, which is what the reconnect test used to
// assert on; r421 deliberately dropped that row (the daemon-disconnect toast
// already covers it) and the discarded title left the test with nothing to
// observe.
func (b *fakeTrayBackend) SetTitle(title string) {
	b.mu.Lock()
	b.title = title
	b.mu.Unlock()
}
func (*fakeTrayBackend) SetTooltip(string) {}
func (*fakeTrayBackend) AddSeparator()     {}

func (b *fakeTrayBackend) titleContains(fragment string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.title, fragment)
}
func (b *fakeTrayBackend) AddMenuItem(title, _ string) tray.ItemHandle {
	return b.addItem(title)
}

func (b *fakeTrayBackend) addItem(title string) *fakeTrayItem {
	item := &fakeTrayItem{backend: b, clicks: make(chan struct{}, 1)}
	b.mu.Lock()
	b.items[title] = item
	b.mu.Unlock()
	return item
}

func (b *fakeTrayBackend) hasItemContaining(fragment string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for title := range b.items {
		if strings.Contains(title, fragment) {
			return true
		}
	}
	return false
}

func (b *fakeTrayBackend) clickItemContaining(fragment string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for title, item := range b.items {
		if strings.Contains(title, fragment) {
			item.clicks <- struct{}{}
			return true
		}
	}
	return false
}

type fakeTrayItem struct {
	backend *fakeTrayBackend
	clicks  chan struct{}
}

func (i *fakeTrayItem) AddSubMenuItem(title, _ string) tray.ItemHandle {
	return i.backend.addItem(title)
}
func (*fakeTrayItem) AddSeparator()              {}
func (*fakeTrayItem) Disable()                   {}
func (i *fakeTrayItem) Clicked() <-chan struct{} { return i.clicks }

type fakeDaemon struct{}

func (fakeDaemon) Hello(context.Context) (*contract.HelloResult, error) {
	return &contract.HelloResult{Capabilities: contract.AllCapabilities}, nil
}
func (fakeDaemon) SystemStatus(context.Context) (*contract.SystemStatusResult, error) {
	return &contract.SystemStatusResult{State: "running"}, nil
}
func (fakeDaemon) RepoList(context.Context) (*contract.RepoListResult, error) {
	return &contract.RepoListResult{}, nil
}
func (fakeDaemon) RepoStatus(context.Context, string) (*contract.RepoStatus, error) {
	return &contract.RepoStatus{}, nil
}
func (fakeDaemon) ErrorList(context.Context, contract.ErrorListPayload) (*contract.ErrorListResult, error) {
	return &contract.ErrorListResult{}, nil
}
func (fakeDaemon) Lock(context.Context, string, []string) (string, error)   { return "", nil }
func (fakeDaemon) Unlock(context.Context, string, []string) (string, error) { return "", nil }
func (fakeDaemon) RepoReservationList(context.Context, string) (*contract.RepoReservationListResult, error) {
	return &contract.RepoReservationListResult{}, nil
}
func (fakeDaemon) RepoReservationRelease(context.Context, contract.RepoReservationReleasePayload) error {
	return nil
}
func (fakeDaemon) RealmAliasClaim(context.Context, string, string) (*contract.RealmAliasClaimResult, error) {
	return &contract.RealmAliasClaimResult{Alias: "test"}, nil
}
func (fakeDaemon) Subscribe(context.Context) (<-chan contract.Event, error) {
	return make(chan contract.Event), nil
}

type updateDaemon struct{ fakeDaemon }

func (updateDaemon) Hello(context.Context) (*contract.HelloResult, error) {
	capabilities := append([]string(nil), contract.AllCapabilities...)
	capabilities = append(capabilities, contract.CapUpdateStatus, contract.CapUpdatePlan, contract.CapUpdateApply)
	return &contract.HelloResult{Capabilities: capabilities}, nil
}

func (updateDaemon) SystemStatus(context.Context) (*contract.SystemStatusResult, error) {
	return &contract.SystemStatusResult{State: "running", Update: &contract.UpdateStatus{
		State: "available", CurrentVersion: "1.0", AvailableVersion: "2.0", ReleaseID: "release-2", RestartRequired: true,
	}}, nil
}

func (updateDaemon) UpdatePlan(context.Context) (*contract.UpdatePlanResult, error) {
	return &contract.UpdatePlanResult{CurrentVersion: "1.0", AvailableVersion: "2.0", ReleaseID: "release-2", RestartRequired: true}, nil
}

func (updateDaemon) UpdateApply(context.Context) (*contract.UpdateApplyResult, error) {
	return &contract.UpdateApplyResult{InstalledVersion: "2.0", RestartRequired: true}, nil
}

func TestRunReturnsRestartRequestAfterConfirmedUpdate(t *testing.T) {
	backend := newFakeTrayBackend()
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), dependencies{
			tray: backend, platform: &platformtest.Fake{ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil }},
			client: updateDaemon{},
		})
	}()
	select {
	case <-backend.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("tray did not become ready")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !backend.clickItemContaining("Zaktualizuj i uruchom ponownie") {
		if time.Now().After(deadline) {
			t.Fatal("update action did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errGUIRestartRequested) {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not request restart")
	}
}

func TestRunStartsAfterTrayReadyAndStopsWithContext(t *testing.T) {
	backend := newFakeTrayBackend()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, dependencies{
			tray:     backend,
			platform: &platformtest.Fake{},
			client:   fakeDaemon{},
		})
	}()

	select {
	case <-backend.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("tray did not become ready")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after context cancellation")
	}

	backend.mu.Lock()
	resets := backend.resets
	backend.mu.Unlock()
	if resets == 0 {
		t.Fatal("initial tray menu was not rendered")
	}
}

func TestRunRejectsIncompleteDependencies(t *testing.T) {
	if err := run(context.Background(), dependencies{}); err == nil {
		t.Fatal("expected incomplete dependencies error")
	}
}

func TestManageAutostartUsesAbsoluteExecutableAndSocket(t *testing.T) {
	backend := &platformtest.Fake{}
	spec := newAutostartSpec(filepath.Join(t.TempDir(), "filees-gui"), "/run/user/1000/filees.sock")
	if err := manageAutostart(context.Background(), backend, "enable", spec, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	snapshot := backend.Snapshot()
	if len(snapshot.AutostartSets) != 1 || !snapshot.AutostartSets[0].Enabled {
		t.Fatalf("autostart calls = %#v", snapshot.AutostartSets)
	}
	got := snapshot.AutostartSets[0].Spec
	if !filepath.IsAbs(got.Executable) || got.ID != "filees-gui" {
		t.Fatalf("autostart spec = %#v", got)
	}
	if len(got.Args) != 2 || got.Args[0] != "--socket" || got.Args[1] != "/run/user/1000/filees.sock" {
		t.Fatalf("autostart args = %#v", got.Args)
	}
}

func TestManageAutostartStatusAndDisable(t *testing.T) {
	backend := &platformtest.Fake{
		AutostartStatusFunc: func(context.Context, platform.AutostartSpec) (platform.AutostartState, error) {
			return platform.AutostartState{Enabled: true, Current: true, Source: "test-source"}, nil
		},
	}
	spec := newAutostartSpec(filepath.Join(t.TempDir(), "filees-gui"), "/tmp/filees.sock")
	var output bytes.Buffer
	if err := manageAutostart(context.Background(), backend, "status", spec, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "autostart: enabled (test-source)\n" {
		t.Fatalf("status output = %q", output.String())
	}
	if err := manageAutostart(context.Background(), backend, "disable", spec, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	snapshot := backend.Snapshot()
	if len(snapshot.AutostartSets) != 1 || snapshot.AutostartSets[0].Enabled {
		t.Fatalf("autostart calls = %#v", snapshot.AutostartSets)
	}
}

func TestManageAutostartRejectsUnknownMode(t *testing.T) {
	backend := &platformtest.Fake{}
	if err := manageAutostart(context.Background(), backend, "surprise", platform.AutostartSpec{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown mode error")
	}
	if snapshot := backend.Snapshot(); len(snapshot.AutostartSets) != 0 || len(snapshot.StatusRequests) != 0 {
		t.Fatalf("backend called for invalid mode: %#v", snapshot)
	}
}

func TestRunRealIPCReconnectsAfterDaemonRestart(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	firstCtx, stopFirst := context.WithCancel(context.Background())
	first := ipcserver.New(socket)
	first.RegisterRepo("alpha", "svn://example/alpha", "/wc/alpha").SetState(contract.StateActive)
	first.RegisterRepo("beta", "svn://example/beta", "/wc/beta").SetState(contract.StateActive)
	if err := first.Start(firstCtx); err != nil {
		t.Fatal(err)
	}
	defer stopFirst()

	backend := newFakeTrayBackend()
	guiCtx, stopGUI := context.WithCancel(context.Background())
	defer stopGUI()
	done := make(chan error, 1)
	go func() {
		done <- run(guiCtx, dependencies{
			tray:     backend,
			platform: &platformtest.Fake{},
			client:   ipcclient.New(socket, "filees-gui-integration-test"),
		})
	}()
	waitFor(t, "two repositories from first daemon", func() bool {
		return backend.hasItemContaining("alpha") && backend.hasItemContaining("beta")
	})

	stopFirst()
	waitFor(t, "GUI disconnect after daemon shutdown", func() bool {
		return backend.titleContains("Brak połączenia")
	})
	waitFor(t, "old socket removal", func() bool {
		_, err := os.Stat(socket)
		return os.IsNotExist(err)
	})

	secondCtx, stopSecond := context.WithCancel(context.Background())
	defer stopSecond()
	second := ipcserver.New(socket)
	second.RegisterRepo("gamma", "svn://example/gamma", "/wc/gamma").SetState(contract.StateActive)
	if err := second.Start(secondCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "repository snapshot from restarted daemon", func() bool {
		return backend.hasItemContaining("gamma") && !backend.hasItemContaining("alpha")
	})
	// Pin the header the other way round too. The status surface is a single
	// string with no other observer, so losing it again would otherwise only
	// show up as a disconnect that is never reported.
	waitFor(t, "connected status back in the tray header", func() bool {
		return backend.titleContains("Połączono")
	})

	stopGUI()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GUI did not stop after context cancellation")
	}
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}
