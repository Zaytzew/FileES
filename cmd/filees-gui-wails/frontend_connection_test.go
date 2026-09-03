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
		// A server the daemon has not fetched from yet is not the same as one it
		// fetched from successfully. Without this the header calls a cached
		// ten-day-old projection current until the first sync happens to fail.
		`return String(server.view_synced_at || "") === "";`,
		`jeszcze niesprawdzone`,
		// server_view_produced_at is still read and still carried, but the
		// header must not render it: we only reach the header after a
		// successful fetch, so the server has just confirmed it is fine, and
		// the client cannot tell "nothing changed" from "something changed and
		// was not published". A number nobody can act on is not a trust signal.
		`server_view_produced_at`,
		// A detached client is refused, not unheard. It is tested before
		// staleness because it is also out of date, and would otherwise be
		// described as a server that will not answer - which sends the reader
		// to check a network that is fine.
		`server.detached === true`,
		`wymagana ponowna aktywacja`,
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
	// The header must not warn about a server that simply had nothing to
	// publish. Both the old wording and its replacement were wrong in the same
	// place: they put an unactionable fact where the reader looks to decide
	// whether to trust the screen.
	for _, forbidden := range []string{"nie publikuje danych", "bez zmian"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("connection header still renders %q", forbidden)
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
