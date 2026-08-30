package main

import (
	"strings"
	"testing"
)

func TestQuarantineFrontendOpensExistingPopupFromRepositoryRow(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	models := embeddedFrontendFile(t, "frontend/bindings/filees/cmd/filees-gui-wails/models.js")

	for _, required := range []string{`repo.can_review_quarantine`, `repoAction("review_quarantine", "Przejrzyj kwarantannę"`, `quarantine: '<svg`} {
		if !strings.Contains(script, required) {
			t.Fatalf("quarantine row action does not contain %q", required)
		}
	}
	if !strings.Contains(models, `this["can_review_quarantine"] = false`) {
		t.Fatal("generated frontend model is missing can_review_quarantine")
	}
}
