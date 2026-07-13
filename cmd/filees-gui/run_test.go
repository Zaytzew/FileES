package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type fakeTrayBackend struct {
	ready chan struct{}
	quit  chan struct{}
	once  sync.Once

	mu     sync.Mutex
	resets int
}

func newFakeTrayBackend() *fakeTrayBackend {
	return &fakeTrayBackend{ready: make(chan struct{}), quit: make(chan struct{})}
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
	b.mu.Unlock()
}
func (*fakeTrayBackend) SetIcon([]byte)                             {}
func (*fakeTrayBackend) SetTitle(string)                            {}
func (*fakeTrayBackend) SetTooltip(string)                          {}
func (*fakeTrayBackend) AddSeparator()                              {}
func (*fakeTrayBackend) AddMenuItem(string, string) tray.ItemHandle { return fakeTrayItem{} }

type fakeTrayItem struct{}

func (fakeTrayItem) AddSubMenuItem(string, string) tray.ItemHandle { return fakeTrayItem{} }
func (fakeTrayItem) AddSeparator()                                 {}
func (fakeTrayItem) Disable()                                      {}
func (fakeTrayItem) Clicked() <-chan struct{}                      { return make(chan struct{}) }

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
