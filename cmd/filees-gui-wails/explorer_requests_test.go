package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/explorermenu"
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/tray"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestExplorerGesturesWaitForPromptRuntime(t *testing.T) {
	source := readMainSource(t)
	applicationStarted := strings.Index(source, "host.Event.OnApplicationEvent(events.Common.ApplicationStarted")
	runtimeReady := strings.Index(source, "promptWindow.OnWindowEvent(events.Common.WindowRuntimeReady")
	runner := strings.Index(source, "gui.runExplorerRequests(host.Context())")
	if runtimeReady < 0 || runner < runtimeReady || runner > applicationStarted {
		t.Fatal("Explorer gestures must wait for the prompt WebView runtime, not merely ApplicationStarted")
	}
	gate := strings.Index(source, "case <-mainProgressReady:")
	if gate < runtimeReady || gate > runner || !strings.Contains(source, "mainProgressReadyOnce.Do(func() { close(mainProgressReady) })") {
		t.Fatal("Explorer folder gestures must also wait for the main progress WebView")
	}
}

func TestMainPanelRaiserWaitsForWindowRuntime(t *testing.T) {
	source := readMainSource(t)
	runtimeReady := strings.Index(source, "mainWindow.RegisterHook(events.Common.WindowRuntimeReady")
	tray := strings.Index(source, "configureWailsTray(host, mainWindow")
	if runtimeReady < 0 || tray < runtimeReady {
		t.Fatal("main window readiness hook missing")
	}
	block := source[runtimeReady:tray]
	end := strings.Index(block, "\n\t})")
	raiser := strings.Index(block, "setMainPanelRaiser(")
	if raiser < 0 || raiser > end {
		t.Fatal("main panel raising must be enabled only inside WindowRuntimeReady, after WebView2 exists")
	}
}

func resetExplorerQueue(t *testing.T) {
	t.Helper()
	for {
		select {
		case request := <-explorerRequests:
			finishExplorerRequest(request)
		default:
			return
		}
	}
}

func TestExplorerVirtualRepeatIsNotQueuedWhileFolderActionRuns(t *testing.T) {
	resetExplorerQueue(t)
	t.Cleanup(func() { resetExplorerQueue(t) })
	path := t.TempDir()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	service := &GUIService{view: guiapp.ViewModel{Connected: true}, explorerFolder: func(context.Context, explorermenu.Request) {
		entered <- struct{}{}
		<-release
	}}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { service.runExplorerRequests(ctx); close(finished) }()
	defer func() { close(release); cancel(); <-finished }()
	request := explorermenu.Request{Verb: explorermenu.Release, Path: path}
	if !queueExplorerRequest(request) {
		t.Fatal("first gesture refused")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler not started")
	}
	if queueExplorerRequest(request) || queueExplorerRequest(explorermenu.Request{Verb: explorermenu.Download, Path: filepath.Join(path, ".")}) {
		t.Fatal("repeat or opposite operation queued while busy")
	}
	if len(explorerRequests) != 0 {
		t.Fatal("duplicate survived in queue")
	}
}

func TestExplorerVirtualRequestsNeverBecomeOrdinaryAdd(t *testing.T) {
	resetExplorerQueue(t)
	t.Cleanup(func() { resetExplorerQueue(t); setMainPanelRaiser(nil) })
	path := t.TempDir()
	if queueExplorerRequest(explorermenu.Request{Verb: "--explorer-delete", Path: path}) {
		t.Fatal("accepted unknown verb")
	}
	actions := make(chan tray.Intent, 4)
	delivered := make(chan explorermenu.Request, 4)
	service := &GUIService{actions: actions, view: guiapp.ViewModel{Connected: true}, explorerFolder: func(_ context.Context, r explorermenu.Request) { delivered <- r }}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { service.runExplorerRequests(ctx); close(finished) }()
	defer func() { cancel(); <-finished }()
	for _, verb := range []string{explorermenu.Download, explorermenu.Release} {
		// Distinct selections test routing, not the same-folder busy guard.
		selection := filepath.Join(path, strings.TrimPrefix(verb, "--"))
		singleInstanceOptions().OnSecondInstanceLaunch(application.SecondInstanceData{Args: []string{"gui", verb, selection}})
		select {
		case r := <-delivered:
			if r.Verb != verb || r.Path != selection {
				t.Fatal(r)
			}
		case <-time.After(time.Second):
			t.Fatal("virtual folder request lost")
		}
	}
	select {
	case a := <-actions:
		t.Fatal("virtual selection became ordinary action", a)
	default:
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
