package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filees/internal/gui/platform"
)

type progressEmitter struct {
	mu     sync.Mutex
	events []ProgressSnapshot
}

func (e *progressEmitter) Emit(name string, data ...any) bool {
	if name != progressEvent || len(data) != 1 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, data[0].(ProgressSnapshot))
	return true
}

func (e *progressEmitter) last() ProgressSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events[len(e.events)-1]
}

// Every stage the controller opens reaches the window with its key, its
// arguments and a start time; closing it - once or twice - takes it away.
func TestProgressServiceEmitsStagesAndTheirEnd(t *testing.T) {
	service := newProgressService()
	emitter := &progressEmitter{}
	service.attachEmitter(emitter)

	closeServer, err := service.ShowProgress(t.Context(), platform.ProgressRequest{PresentationKey: "progress.createRepository.server", PresentationArgs: map[string]string{"name": "Projekt", "path": `C:\Projekt`}, Title: "Tworzenie repozytorium", Text: "Projekt"})
	if err != nil {
		t.Fatal(err)
	}
	closeImport, _ := service.ShowProgress(t.Context(), platform.ProgressRequest{PresentationKey: "progress.createRepository.import"})
	items := emitter.last().Items
	if len(items) != 2 || items[0].PresentationKey != "progress.createRepository.server" || items[0].PresentationArgs["name"] != "Projekt" || items[0].StartedAt == "" || items[1].PresentationKey != "progress.createRepository.import" {
		t.Fatalf("items = %+v", items)
	}
	closeServer()
	closeServer()
	if items := emitter.last().Items; len(items) != 1 || items[0].PresentationKey != "progress.createRepository.import" {
		t.Fatalf("after closing the server stage: %+v", items)
	}
	closeImport()
	if items := emitter.last().Items; len(items) != 0 {
		t.Fatalf("after the last stage: %+v", items)
	}
}

// The overlay exists only if app.js starts it and every language words it.
func TestProgressOverlayIsWiredAndTranslated(t *testing.T) {
	app, err := os.ReadFile(filepath.Join("frontend", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "initializeProgressOverlay(") {
		t.Fatal("app.js does not start the progress overlay")
	}
	keys := []string{
		"progress.createRepository.server.title", "progress.createRepository.server.text",
		"progress.createRepository.import.title", "progress.createRepository.import.text",
		"progress.attach.title", "progress.attach.text",
		"progress.queue", "progress.published", "progress.elapsed", "progress.background", "progress.show",
	}
	for _, locale := range []string{"pl", "en", "de", "fr", "es"} {
		raw, err := os.ReadFile(filepath.Join("frontend", "locales", locale+".js"))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			if !strings.Contains(string(raw), `"`+key+`"`) {
				t.Errorf("%s lacks %s", locale, key)
			}
		}
	}
}
