package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestPromptNamedBindingsMatchFrontend(t *testing.T) {
	_ = application.New(application.Options{})
	bindings := application.NewBindings(nil, nil)
	if err := bindings.Add(application.NewService(newPromptBridge(newPromptService()))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"filees/cmd/filees-gui-wails.PromptBridge.Snapshot",
		"filees/cmd/filees-gui-wails.PromptBridge.Resolve",
		"filees/cmd/filees-gui-wails.PromptBridge.Cancel",
	} {
		method := bindings.Get(&application.CallOptions{MethodName: name})
		if method == nil {
			t.Fatalf("named binding not found: %s", name)
		}
		t.Logf("%s=%d", name, method.ID)
	}
	if bindings.Get(&application.CallOptions{MethodName: "filees/cmd/filees-gui-wails.PromptBridge.Resolve"}).ID != 3660384409 {
		t.Fatal("PromptBridge.Resolve binding ID changed; regenerate promptservice.js")
	}
}

func TestPromptFrontendIncludesServiceBinding(t *testing.T) {
	index, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/index.js")
	if err != nil || !strings.Contains(string(index), "PromptService") {
		t.Fatalf("prompt binding missing from frontend index: %v", err)
	}
	module, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/promptservice.js")
	if err != nil || !strings.Contains(string(module), "ByID(3660384409") {
		t.Fatalf("prompt service module is incomplete: %v", err)
	}
}

func TestPromptServiceReturnsBrowserChoice(t *testing.T) {
	service := newPromptService()
	shown := make(chan struct{}, 1)
	hideBlocked := make(chan struct{})
	service.attachPresentation(func() { shown <- struct{}{} }, func() { <-hideBlocked })
	defer close(hideBlocked)
	result := make(chan platform.PromptTextResult, 1)
	go func() {
		got, _ := service.PromptText(context.Background(), platform.PromptTextRequest{Title: "Limit", Default: "30"})
		result <- got
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("prompt window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "text" || snapshot.Default != "30" || snapshot.Revision == 0 {
		t.Fatalf("unexpected projection: %+v", snapshot)
	}
	if accepted := service.Resolve(PromptChoice{Revision: snapshot.Revision, Confirmed: true, Value: "90"}); !accepted.Accepted {
		t.Fatalf("choice rejected: %+v", accepted)
	}
	select {
	case got := <-result:
		if got.Cancelled || got.Value != "90" {
			t.Fatalf("unexpected result: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt did not resolve")
	}
}
