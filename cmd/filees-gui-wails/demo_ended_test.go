package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// After a demo realm expires the panel explains the way on instead of an empty
// "no folders" (owner's acceptance, 2026-09-24): an invitation through the
// ordinary activation, or filees.space in the system browser - that one fixed
// address and nothing a snapshot could inject.
func TestDemoEndedPanelOffersTheWayOn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("frontend", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	app := string(raw)
	for _, required := range []string{
		`import { Browser, Events, Window } from "/wails/runtime.js";`,
		`item.cause === "demo_expired"`,
		`demoEnded(snapshot) ? demoEndedHTML() :`,
		`data-global-action="activation"`,
		`data-action="activate"`,
		`const FILEES_SITE = "https://filees.space/";`,
		`if (external.dataset.externalUrl === FILEES_SITE) Browser.OpenURL(FILEES_SITE)`,
	} {
		if !strings.Contains(app, required) {
			t.Errorf("app.js lacks %q", required)
		}
	}
	if strings.Count(app, "Browser.OpenURL(") != 1 {
		t.Fatal("the panel opens the system browser from somewhere other than the fixed filees.space link")
	}
	for _, locale := range []string{"pl", "en", "de", "fr", "es"} {
		catalogue, err := os.ReadFile(filepath.Join("frontend", "locales", locale+".js"))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"title", "files", "next", "activate", "learn"} {
			if !strings.Contains(string(catalogue), `"demoEnded.`+key+`"`) {
				t.Errorf("%s lacks demoEnded.%s", locale, key)
			}
		}
	}
}
