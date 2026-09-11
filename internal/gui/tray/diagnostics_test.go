package tray

import (
	"strings"
	"testing"

	app "filees/internal/gui/app"
)

// The tray shows what a person can act on, never the daemon's raw diagnostics.
//
// This is the rule internal/gui/app's model used to enforce by dropping the
// field entirely. The full journal needs that text, so the field is now
// carried and the boundary lives here, where the tray model is actually built.
func TestTrayNeverCarriesRawDiagnostics(t *testing.T) {
	const diagnostic = "RAW-DIAGNOSTIC-MUST-NOT-REACH-THE-TRAY"
	menu := BuildMenu(app.ViewModel{
		Connected: true,
		Repos:     []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Errors: []app.ErrorViewModel{{
			ID: "e1", RepoID: "docs", Timestamp: "2026-08-10T12:01:00Z",
			Severity: "ERROR", Code: "LOCK-1", Message: "Plik jest zablokowany",
			Hint: "REQUIRE_ACTION", Details: diagnostic,
		}},
	})
	var walk func(items []MenuItemModel)
	walk = func(items []MenuItemModel) {
		for _, item := range items {
			if strings.Contains(item.Title, diagnostic) || strings.Contains(item.Tooltip, diagnostic) {
				t.Fatalf("raw diagnostics reached the tray: %+v", item)
			}
			walk(item.Children)
		}
	}
	walk(menu.Items)
	if strings.Contains(menu.Tooltip, diagnostic) || strings.Contains(menu.Title, diagnostic) {
		t.Fatalf("raw diagnostics reached the tray header: %+v", menu)
	}
}
