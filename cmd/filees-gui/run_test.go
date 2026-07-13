package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
func (*fakeTrayBackend) SetIcon([]byte)    {}
func (*fakeTrayBackend) SetTitle(string)   {}
func (*fakeTrayBackend) SetTooltip(string) {}
func (*fakeTrayBackend) AddSeparator()     {}
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

func (b *fakeTrayBackend) click(title string) bool {
	b.mu.Lock()
	item := b.items[title]
	b.mu.Unlock()
	if item == nil {
		return false
	}
	item.clicks <- struct{}{}
	return true
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
func (fakeDaemon) Subscribe(context.Context) (<-chan contract.Event, error) {
	return make(chan contract.Event), nil
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

func TestRunRealIPCReconnectsAfterDaemonRestart(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	firstCtx, stopFirst := context.WithCancel(context.Background())
	first := ipcserver.New(socket)
	first.RegisterRepo("alpha", "svn://example/alpha", t.TempDir()).SetState(contract.StateActive)
	first.RegisterRepo("beta", "svn://example/beta", t.TempDir()).SetState(contract.StateActive)
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
		return backend.hasItemContaining("Brak połączenia")
	})
	waitFor(t, "old socket removal", func() bool {
		_, err := os.Stat(socket)
		return os.IsNotExist(err)
	})

	secondCtx, stopSecond := context.WithCancel(context.Background())
	defer stopSecond()
	second := ipcserver.New(socket)
	second.RegisterRepo("gamma", "svn://example/gamma", t.TempDir()).SetState(contract.StateActive)
	if err := second.Start(secondCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "repository snapshot from restarted daemon", func() bool {
		return backend.hasItemContaining("gamma") && !backend.hasItemContaining("alpha")
	})

	waitFor(t, "quit menu item", func() bool { return backend.click("Zamknij GUI") })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GUI did not stop after quit intent")
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
