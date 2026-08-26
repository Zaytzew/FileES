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
	// UWAGA: ID liczone jest ze skrotu pelnej nazwy metody, a ta zawiera
	// sciezke pakietu. Pod "go test" pakiet nazywa sie
	// filees/cmd/filees-gui-wails, ale w zbudowanym pliku wykonywalnym jest
	// to po prostu "main" - wiec ID sa tu INNE niz te, ktorych uzywa
	// dzialajaca aplikacja. Nie wolno pinowac tutejszej wartosci jako
	// oczekiwanej dla frontendu; frontend musi miec ID z kontekstu budowania
	// (patrz TestPromptFrontendIncludesServiceBinding).
	if bindings.Get(&application.CallOptions{MethodName: "filees/cmd/filees-gui-wails.PromptBridge.Resolve"}) == nil {
		t.Fatal("PromptBridge.Resolve nie jest zwiazane")
	}
}

func TestPromptFrontendIncludesServiceBinding(t *testing.T) {
	index, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/index.js")
	if err != nil || !strings.Contains(string(index), "PromptService") {
		t.Fatalf("prompt binding missing from frontend index: %v", err)
	}
	module, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/promptservice.js")
	if err != nil || !strings.Contains(string(module), "ByID(2590249023") {
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

func TestPromptServiceDoesNotHideFollowingPrompt(t *testing.T) {
	service := newPromptService()
	shown := make(chan struct{}, 2)
	hidden := make(chan struct{}, 2)
	service.attachPresentation(func() { shown <- struct{}{} }, func() { hidden <- struct{}{} })

	firstResult := make(chan platform.PromptTextResult, 1)
	go func() {
		got, _ := service.PromptText(context.Background(), platform.PromptTextRequest{Title: "Adres"})
		firstResult <- got
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("first prompt window was not shown")
	}
	first := service.Snapshot()
	if accepted := service.Resolve(PromptChoice{Revision: first.Revision, Confirmed: true, Value: "publiczny-adres"}); !accepted.Accepted {
		t.Fatalf("first choice rejected: %+v", accepted)
	}
	select {
	case <-firstResult:
	case <-time.After(time.Second):
		t.Fatal("first prompt did not resolve")
	}

	secondResult := make(chan platform.PromptTextResult, 1)
	go func() {
		got, _ := service.PromptText(context.Background(), platform.PromptTextRequest{Title: "Odbiorcy"})
		secondResult <- got
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("following prompt window was not shown")
	}
	second := service.Snapshot()
	if second.Revision == first.Revision || second.Title != "Odbiorcy" {
		t.Fatalf("following projection was not published: %+v", second)
	}
	select {
	case <-hidden:
		t.Fatal("completion of the first prompt hid the following prompt")
	case <-time.After(180 * time.Millisecond):
	}

	service.Cancel()
	select {
	case got := <-secondResult:
		if !got.Cancelled {
			t.Fatalf("cancel result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("following prompt did not cancel")
	}
	select {
	case <-hidden:
	case <-time.After(time.Second):
		t.Fatal("final prompt window was not hidden")
	}
}

func TestPromptFrontendLeavesVisibilityToPromptService(t *testing.T) {
	source, err := frontend.ReadFile("frontend/prompt.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "Window.Hide()") {
		t.Fatal("prompt frontend must not race the service by hiding the shared window")
	}
	for _, wanted := range []string{"revealFollowingPrompt", "PromptService.Snapshot()", "Window.Show()", "Window.Focus()"} {
		if !strings.Contains(string(source), wanted) {
			t.Fatalf("prompt frontend is missing resilient hand-off %q", wanted)
		}
	}
}
