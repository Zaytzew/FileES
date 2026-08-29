package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/platform"
)

type settingsEmitter struct {
	mu   sync.Mutex
	name string
	data SettingsSnapshot
}

func (emitter *settingsEmitter) Emit(name string, data ...any) bool {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	emitter.name = name
	if len(data) == 1 {
		emitter.data, _ = data[0].(SettingsSnapshot)
	}
	return true
}

func TestSettingsServiceProjectsScopedServerAndReturnsValidatedChoice(t *testing.T) {
	service := newSettingsService()
	emitter := &settingsEmitter{}
	shown := make(chan struct{}, 1)
	hidden := make(chan struct{}, 2)
	service.attachEmitter(emitter)
	service.attachPresentation(func() { shown <- struct{}{} }, func() { hidden <- struct{}{} })

	request := platform.SettingsDialogRequest{
		Title: "FileES — spot", Text: "Wybierz działanie dla tego serwera.",
		Servers: []platform.SettingsServer{{
			ID: "spot", Name: "spot", Address: "spot.example.net", Realm: "acme", ClientID: "client-1",
			SessionTimeoutMin: 30, CanSetSessionTimeout: true,
			Folders: []platform.SettingsFolder{{ID: "docs", Name: "Dokumenty", LocalPath: `E:\Dokumenty`, State: "aktywne", Access: "odczyt i zapis", Editing: "swobodna"}},
		}},
	}
	resultCh := make(chan platform.SettingsDialogResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := (settingsBrowserAdapter{service: service}).ShowSettings(context.Background(), request)
		resultCh <- result
		errCh <- err
	}()

	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("settings window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Revision != 1 || snapshot.Server.ID != "spot" || !snapshot.Server.CanSetSessionTimeout {
		t.Fatalf("settings snapshot = %+v", snapshot)
	}
	if got := service.Choose(SettingsChoice{Action: "session_timeout", ServerID: "other"}); got.Accepted || got.Code != "settings_context_changed" {
		t.Fatalf("foreign context accepted: %+v", got)
	}
	if got := service.Choose(SettingsChoice{Action: "delete_repository", ServerID: "spot"}); got.Accepted || got.Code != "settings_action_unavailable" {
		t.Fatalf("unprojected action accepted: %+v", got)
	}
	if got := service.Choose(SettingsChoice{Action: "session_timeout", ServerID: "spot"}); !got.Accepted {
		t.Fatalf("projected action rejected: %+v", got)
	}

	select {
	case result := <-resultCh:
		if result.Action != platform.SettingsDialogSessionTimeout || result.ServerID != "spot" {
			t.Fatalf("settings result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("settings browser did not return")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("settings browser error: %v", err)
	}
	select {
	case <-hidden:
	case <-time.After(time.Second):
		t.Fatal("settings window was not hidden")
	}

	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.name != settingsSnapshotEvent || emitter.data.Server.ID != "spot" {
		t.Fatalf("settings event = %q / %+v", emitter.name, emitter.data)
	}
}

func TestSettingsServiceRejectsUnscopedMultiServerWizard(t *testing.T) {
	service := newSettingsService()
	request := platform.SettingsDialogRequest{Servers: []platform.SettingsServer{{ID: "one"}, {ID: "two"}}}
	result, err := (settingsBrowserAdapter{service: service}).ShowSettings(context.Background(), request)
	if err != nil || result.Action != platform.SettingsDialogClose || service.Snapshot().Revision != 0 {
		t.Fatalf("multi-server request = result %+v err %v snapshot %+v", result, err, service.Snapshot())
	}
}

func TestSettingsServiceProjectsOnlyAuthorisedServerActions(t *testing.T) {
	service := newSettingsService()
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	resultCh := make(chan platform.SettingsDialogResult, 1)
	go func() {
		result, _ := (settingsBrowserAdapter{service: service}).ShowSettings(context.Background(), platform.SettingsDialogRequest{Servers: []platform.SettingsServer{{
			ID: "spot", CanSetRealmVisibility: true, CanSetRealmBranding: false, CanAddFolder: true,
		}}})
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("settings window was not shown")
	}
	actions := service.Snapshot().Server.Actions
	if len(actions) != 4 || actions[0].ID != "realm_visibility" || actions[1].ID != "add_folder" || actions[2].ID != "detach_server" || actions[3].ID != "remove_realm" {
		t.Fatalf("server actions = %+v", actions)
	}
	if got := service.Choose(SettingsChoice{Action: "realm_branding", ServerID: "spot"}); got.Accepted || got.Code != "settings_action_unavailable" {
		t.Fatalf("unavailable branding accepted: %+v", got)
	}
	if got := service.Choose(SettingsChoice{Action: "add_folder", ServerID: "spot"}); !got.Accepted {
		t.Fatalf("add folder rejected: %+v", got)
	}
	select {
	case result := <-resultCh:
		if result.Action != platform.SettingsDialogAddFolder || result.ServerID != "spot" {
			t.Fatalf("server action result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("settings browser did not return")
	}
}

func TestSettingsServiceKeepsPairingOutOfServerWindow(t *testing.T) {
	snapshot, ok := projectSettingsRequest(platform.SettingsDialogRequest{Servers: []platform.SettingsServer{{ID: "spot", CanClaimRealmAlias: true, CanPairMobile: true}}})
	if !ok || len(snapshot.Server.Actions) != 3 || snapshot.Server.Actions[0].ID != "realm_alias" || snapshot.Server.Actions[1].ID != "detach_server" || snapshot.Server.Actions[2].ID != "remove_realm" {
		t.Fatalf("alias/mobile actions = ok %v actions %+v", ok, snapshot.Server.Actions)
	}
	html, err := frontend.ReadFile("frontend/settings.html")
	if err != nil || strings.Contains(string(html), "settings-folders") || strings.Contains(string(html), ">Foldery<") {
		t.Fatalf("server settings still duplicate folders: %v", err)
	}
}
