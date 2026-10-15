package journal

import (
	"filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
	"testing"
	"time"
)

func TestSessionMarkersAreGlobalInformationalChronology(t *testing.T) {
	vm := app.ViewModel{Sessions: []contract.SessionRecord{
		{ID: "old", Phase: "stopping", Version: "old", At: "2026-10-15T21:57:02Z"},
		{ID: "new", Phase: "started", Version: "new", At: "2026-10-15T21:57:09Z"},
		{ID: "future", Phase: "unknown", At: "2026-10-15T21:57:10Z"},
	}}
	entries := BuildAt(vm, time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC), Texts{})
	if len(entries) != 2 || entries[0].Summary != "Uruchomiono FileES" || entries[1].Summary != "Planowe zatrzymanie FileES" {
		t.Fatal(entries)
	}
	for _, e := range entries {
		if e.Repo != "FileES" || e.Severity != "info" || e.Emphasized || e.Diagnostics != "" {
			t.Fatal(e)
		}
	}
}
