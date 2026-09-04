package main

import (
	"strings"
	"testing"
)

func TestRecoveryArchiveRowOffersLocalDismissal(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")
	models := embeddedFrontendFile(t, "frontend/bindings/filees/cmd/filees-gui-wails/models.js")

	for _, required := range []string{`repo.can_dismiss_recovery`, `repoAction("dismiss_recovery", "Usuń archiwum z tego klienta"`, `remove: '<svg`} {
		if !strings.Contains(script, required) {
			t.Fatalf("recovery dismissal row action does not contain %q", required)
		}
	}
	if !strings.Contains(styles, ".repo-icon-action.recovery-dismiss:hover") {
		t.Fatal("recovery dismissal has no destructive hover treatment")
	}
	if !strings.Contains(models, `this["can_dismiss_recovery"] = undefined`) {
		t.Fatal("generated frontend model is missing can_dismiss_recovery")
	}
}
