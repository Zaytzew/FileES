package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/explorermenu"
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/tray"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func resetExplorerQueue(t *testing.T) {
	t.Helper()
	for {
		select {
		case <-explorerRequests:
		default:
			return
		}
	}
}

func TestSecondLaunchForwardsUnicodeGestureAndStartupDoesNotLoseIt(t *testing.T) {
	resetExplorerQueue(t)
	t.Cleanup(func() { resetExplorerQueue(t); setMainPanelRaiser(nil) })
	setMainPanelRaiser(nil)
	path := filepath.Join(t.TempDir(), "Żółć i zdjęcia")
	singleInstanceOptions().OnSecondInstanceLaunch(application.SecondInstanceData{Args: []string{"gui", explorermenu.Detach, path}})
	select {
	case got := <-explorerRequests:
		if got.Verb != explorermenu.Detach || got.Path != path {
			t.Fatalf("request=%+v", got)
		}
	default:
		t.Fatal("cold gesture lost")
	}
	singleInstanceOptions().OnSecondInstanceLaunch(application.SecondInstanceData{Args: []string{"gui", explorermenu.Add, path, "--delete"}})
	select {
	case <-explorerRequests:
		t.Fatal("forwarded arbitrary arguments")
	default:
	}
}

func TestExplorerQueueBoundedAndDeliveredOnlyAfterFreshProjection(t *testing.T) {
	resetExplorerQueue(t)
	t.Cleanup(func() { resetExplorerQueue(t) })
	path := t.TempDir()
	for i := 0; i < cap(explorerRequests); i++ {
		if !queueExplorerRequest(explorermenu.Request{Verb: explorermenu.Add, Path: path}) {
			t.Fatal("queue too small")
		}
	}
	if queueExplorerRequest(explorermenu.Request{Verb: explorermenu.Add, Path: path}) {
		t.Fatal("unbounded queue")
	}
	resetExplorerQueue(t)
	actions := make(chan tray.Intent, 1)
	service := &GUIService{actions: actions}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { service.runExplorerRequests(ctx); close(finished) }()
	queueExplorerRequest(explorermenu.Request{Verb: explorermenu.Add, Path: path})
	select {
	case <-actions:
		t.Fatal("sent before fresh state")
	case <-time.After(130 * time.Millisecond):
	}
	service.mu.Lock()
	service.view = guiapp.ViewModel{Connected: true}
	service.mu.Unlock()
	select {
	case got := <-actions:
		if got.Kind != tray.IntentExplorerAdd || got.Path != path {
			t.Fatalf("intent=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("gesture not delivered")
	}
	cancel()
	<-finished
}
