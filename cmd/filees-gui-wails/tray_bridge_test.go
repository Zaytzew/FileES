package main

import (
	"testing"

	guiapp "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
)

func TestProjectWailsTrayTracksConnectionRepositoriesAndLocks(t *testing.T) {
	projection := projectWailsTray(Snapshot{
		Connected: true, IconState: string(guiapp.IconActive),
		Capabilities: []string{contract.CapSystemRestart, contract.CapSystemShutdown},
		Servers:      []ServerProjection{{ID: "server", ReservationsKnown: true}},
		Repositories: []RepoProjection{{ID: "one"}, {ID: "two"}},
		Reservations: []ReservationProjection{{ID: "lock"}},
	})
	if projection.Icon != guiapp.IconActive || projection.Status != "Połączono · 2 repo · 1 blokada" || projection.Tooltip == "" {
		t.Fatalf("projection = %+v", projection)
	}
	if !projection.CanRestart || !projection.CanShutdown {
		t.Fatalf("lifecycle unavailable: %+v", projection)
	}

	disconnected := projectWailsTray(Snapshot{})
	if disconnected.Icon != guiapp.IconDisconnected || disconnected.Status != "Rozłączono · 0 repo · 0 blokad" {
		t.Fatalf("disconnected = %+v", disconnected)
	}

	unknown := projectWailsTray(Snapshot{Connected: true, Servers: []ServerProjection{{ID: "server"}}})
	if unknown.Status != "Połączono · 0 repo · ? blokad" {
		t.Fatalf("unknown reservations = %+v", unknown)
	}

	stale := projectWailsTray(Snapshot{
		Connected: true, Stale: true,
		Capabilities: []string{contract.CapSystemRestart, contract.CapSystemShutdown},
	})
	if stale.CanRestart || stale.CanShutdown {
		t.Fatalf("stale lifecycle available: %+v", stale)
	}
}
