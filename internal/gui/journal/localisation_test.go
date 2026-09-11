package journal

import (
	"strings"
	"testing"
	"time"

	"filees/internal/gui/app"
)

func errorViewModel() app.ViewModel {
	return app.ViewModel{
		Repos: []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Errors: []app.ErrorViewModel{{
			ID: "e1", RepoID: "docs", Timestamp: "2026-09-11T10:00:00Z",
			Code: "LAB-9999", Severity: "ERROR", Hint: "REQUIRE_ACTION",
			MessageKey: "lab.unknown", Message: "[LAB-9999 lab.unknown]",
			Details: "DIAGNOSTIC-LAB-ONLY",
		}},
	}
}

// The error marker is interface wording, so it comes from the renderer's
// catalogue. It used to be a literal, which left "⚠ BŁĄD" standing in an
// otherwise English journal.
func TestErrorPrefixComesFromTheInterfaceCatalogue(t *testing.T) {
	texts := Texts{
		Chrome: func(key, fallback string) string {
			if key == "journal.errorPrefix" {
				return "⚠ ERROR"
			}
			return fallback
		},
	}
	entries := BuildAt(errorViewModel(), time.Now(), texts)
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	if !strings.HasPrefix(entries[0].Summary, "⚠ ERROR · ") {
		t.Fatalf("summary = %q; the marker was not taken from the catalogue", entries[0].Summary)
	}
	if strings.Contains(entries[0].Summary, "BŁĄD") {
		t.Fatalf("summary = %q; the Polish literal survived", entries[0].Summary)
	}
}

// A resolver that answers with nothing must not blank the marker out.
func TestErrorPrefixFallsBackWhenTheCatalogueIsSilent(t *testing.T) {
	texts := Texts{Chrome: func(string, string) string { return "" }}
	entries := BuildAt(errorViewModel(), time.Now(), texts)
	if !strings.Contains(entries[0].Summary, "⚠") {
		t.Fatalf("summary = %q; the marker disappeared", entries[0].Summary)
	}
}

// The hint sentence belongs to the daemon's catalogue, not to this package.
func TestHintComesFromTheDaemonCatalogue(t *testing.T) {
	texts := Texts{Hint: func(hint string) string {
		if hint == "REQUIRE_ACTION" {
			return "your decision is needed"
		}
		return ""
	}}
	entries := BuildAt(errorViewModel(), time.Now(), texts)
	if !strings.Contains(entries[0].Details, "your decision is needed") {
		t.Fatalf("details = %q; the hint did not come from the catalogue", entries[0].Details)
	}
}

// The daemon's raw text reaches the full journal, separately from the guidance
// a person can act on. For a fault the catalogue cannot name it is the only
// record of what happened.
func TestRawDiagnosticsReachTheJournalSeparately(t *testing.T) {
	entries := BuildAt(errorViewModel(), time.Now(), testTexts())
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].Diagnostics != "DIAGNOSTIC-LAB-ONLY" {
		t.Fatalf("diagnostics = %q", entries[0].Diagnostics)
	}
	// Kept out of Details so a tray tooltip built from it cannot leak them.
	if strings.Contains(entries[0].Details, "DIAGNOSTIC-LAB-ONLY") {
		t.Fatalf("details = %q; raw diagnostics must not be mixed into guidance", entries[0].Details)
	}
	if strings.Contains(entries[0].Summary, "DIAGNOSTIC-LAB-ONLY") {
		t.Fatalf("summary = %q; raw diagnostics must never enter a sentence", entries[0].Summary)
	}
}
