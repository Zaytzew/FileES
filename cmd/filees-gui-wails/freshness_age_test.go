package main

import (
	"testing"

	guiapp "filees/internal/gui/app"
)

// The age shown for an unreachable server is how long ago the server last
// confirmed the data, not when it last regenerated an unchanged view.
func TestFreshnessAgeIsTheLastConfirmation(t *testing.T) {
	vm := guiapp.ViewModel{Connected: true, Servers: []guiapp.ServerViewModel{{
		ID: "cloud", DisplayName: "atmprojekt:filees",
		ViewGeneratedAt: "2026-09-23T12:00:00Z", ViewSyncedAt: "2026-09-24T13:10:00Z",
		ViewSyncFailures: 1, ViewSyncError: "not responding",
	}}}
	got := projectFreshness(vm)
	if got.State != "server_unavailable" || got.Since != "2026-09-24T13:10:00Z" {
		t.Fatalf("freshness = %+v, want the last confirmation as its age", got)
	}
	vm.Servers[0].ViewSyncedAt = ""
	if got := projectFreshness(vm); got.Since != "2026-09-23T12:00:00Z" {
		t.Fatalf("without a confirmation the generation time is the fallback: %+v", got)
	}
}
