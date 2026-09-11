package main

import (
	"strings"
	"testing"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/journal"
)

// The supported tray is this one, not the closed Fyne branch, so the boundary
// is asserted here too: raw daemon diagnostics reach the full journal and
// nothing else. The tray is a glance, and machine text pasted into a tooltip
// is neither readable nor actionable.
func TestWailsTrayAndSnapshotKeepDiagnosticsOutOfTheGlance(t *testing.T) {
	const diagnostic = "RAW-DIAGNOSTIC-MUST-NOT-REACH-THE-TRAY"
	vm := guiapp.ViewModel{
		Connected: true,
		Repos:     []guiapp.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Errors: []guiapp.ErrorViewModel{{
			ID: "e1", RepoID: "docs", Timestamp: "2026-09-11T10:00:00Z",
			Code: "LAB-9999", Severity: "ERROR", Hint: "REQUIRE_ACTION",
			MessageKey: "lab.unknown", Message: "[LAB-9999 lab.unknown]",
			Details: diagnostic,
		}},
	}
	snapshot := projectViewModel(vm, journal.Texts{})

	if len(snapshot.Journal) != 1 {
		t.Fatalf("journal = %#v", snapshot.Journal)
	}
	entry := snapshot.Journal[0]
	if entry.Diagnostics != diagnostic {
		t.Fatalf("the full journal lost the diagnostic: %#v", entry)
	}
	// It travels in its own field, so no summary or guidance can carry it into
	// a surface that only renders those.
	if strings.Contains(entry.Summary, diagnostic) || strings.Contains(entry.Details, diagnostic) {
		t.Fatalf("the diagnostic leaked into the sentence or the guidance: %#v", entry)
	}

	projection := projectWailsTray(snapshot)
	if strings.Contains(projection.Status, diagnostic) || strings.Contains(projection.Tooltip, diagnostic) {
		t.Fatalf("raw diagnostics reached the tray: %#v", projection)
	}
}
