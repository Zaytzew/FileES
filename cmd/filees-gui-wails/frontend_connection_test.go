package main

import (
	"strings"
	"testing"
)

func TestFrontendMakesDaemonProjectionFreshnessExplicit(t *testing.T) {
	index := embeddedFrontendFile(t, "frontend/index.html")
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")

	for _, wanted := range []string{`id="offline-copy"`, `id="projection-freshness"`} {
		if !strings.Contains(index, wanted) {
			t.Fatalf("frontend connection projection is missing %q", wanted)
		}
	}
	for _, wanted := range []string{
		`freshness.textContent = "Projekcja lokalna bieżąca"`,
		`freshness.textContent = "Projekcja lokalna jest weryfikowana"`,
		`freshness.textContent = "Projekcja lokalna niezweryfikowana"`,
		`Pokazujemy ostatnią pełną projekcję z ${shortDateTime(snapshot.last_refresh)}`,
		`Nie ma jeszcze zapisanej pełnej projekcji`,
		`Brak zapisanej projekcji; panel odświeży się automatycznie.`,
		`Projekcja jest niezweryfikowana.`,
		`ostatni znany stan · demon offline`,
	} {
		if !strings.Contains(script, wanted) {
			t.Fatalf("frontend connection renderer is missing %q", wanted)
		}
	}
	if !strings.Contains(styles, `.pulse-card[data-connection="offline"] .pulse-caption`) {
		t.Fatal("offline daemon does not take visual priority on the radar")
	}
}
