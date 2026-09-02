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
		`freshness.textContent = "Stan danych: aktualny"`,
		`freshness.textContent = "Aktualizowanie danych"`,
		`freshness.textContent = "Demon niedostępny — dane niepotwierdzone"`,
		// The header must consult the per-server view age, not only the link to
		// the daemon. Without this the strings above can be restored on top of
		// the old measurement and the test would go green again.
		`const stale = snapshot.connected ? staleViewServers(snapshot) : [];`,
		`view_sync_failures`,
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
	// The sentence that made this necessary. It claimed the projection was
	// current while measuring the GUI-to-daemon link, so on 2026-09-02 every
	// repository on a server whose view had been refused for ten days rendered
	// as up to date. Its absence is the guarantee; the replacements above are
	// only the wording.
	if strings.Contains(script, "Projekcja lokalna bieżąca") {
		t.Fatal("the header must not claim the projection is current from the daemon link alone")
	}
	if !strings.Contains(styles, `.pulse-card[data-connection="offline"] .pulse-caption`) {
		t.Fatal("offline daemon does not take visual priority on the radar")
	}
}
