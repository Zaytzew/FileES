package main

import (
	"strings"
	"testing"
)

func TestRemoteDeletionFrontendPreservesLocalWorkDistinction(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	models := embeddedFrontendFile(t, "frontend/bindings/filees/cmd/filees-gui-wails/models.js")
	for _, text := range []string{"repo.local_copy_preserved", "lokalne pliki zachowane", "zachowano niewysłane zmiany", "sprawdź zachowany folder"} {
		if !strings.Contains(script, text) {
			t.Fatalf("missing terminal presentation %q", text)
		}
	}
	for _, field := range []string{"local_copy_preserved", "local_copy_status"} {
		if !strings.Contains(models, `this["`+field+`"] = undefined`) {
			t.Fatalf("missing binding %s", field)
		}
	}
}
