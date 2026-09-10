package main

import (
	"os"
	"strings"
	"testing"
)

func TestNativeLanguageUsesEmbeddedGUICatalogues(t *testing.T) {
	language, err := loadNativeLanguage()
	if err != nil {
		t.Fatal(err)
	}
	if got := language.text("tray.show"); got != "Show panel" {
		t.Fatal(got)
	}
	if !language.selectLocale("pl") {
		t.Fatal("Polish not selected")
	}
	if got := language.text("tray.show"); got != "Pokaż panel" {
		t.Fatal(got)
	}
	for _, invalid := range []string{"../../private", "system", "", "pl-PL"} {
		if language.selectLocale(invalid) || language.locale != "pl" {
			t.Fatalf("accepted unresolved locale %q", invalid)
		}
	}
	if language.selectLocale("pl") {
		t.Fatal("unchanged preference must be a no-op")
	}
	for locale := range language.catalogues {
		language.selectLocale(locale)
		for _, key := range []string{"tray.starting", "tray.show", "tray.announcements", "tray.refresh", "tray.activate", "tray.restart", "tray.quit"} {
			if got := language.text(key); got == "" || strings.HasPrefix(got, "[") {
				t.Fatalf("%s %s: %q", locale, key, got)
			}
		}
	}
	if got := language.text("missing"); got != "[missing]" {
		t.Fatal(got)
	}
	delete(language.catalogues["pl"], "tray.show")
	language.selectLocale("pl")
	if got := language.text("tray.show"); got != "Show panel" {
		t.Fatal(got)
	}
}

func TestNativeLanguageEventDoesNotReplayActionsOrNotifications(t *testing.T) {
	data, err := os.ReadFile("tray_bridge.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "host.Event.On(nativeLanguageEvent,")
	if start < 0 {
		t.Fatal("missing language handler")
	}
	end := strings.Index(source[start:], "\n\t})")
	if start < 0 || end < 0 {
		t.Fatal("missing language handler")
	}
	handler := source[start : start+end]
	if !strings.Contains(handler, `event.Sender != "filees-main"`) {
		t.Fatal("missing main-window boundary")
	}
	for _, forbidden := range []string{"Observe(", "Notify(", "Trigger(", "Refresh(", "SetHidden("} {
		if strings.Contains(handler, forbidden) {
			t.Fatalf("language event performs %s", forbidden)
		}
	}
}
