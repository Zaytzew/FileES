package main

import (
	"testing"

	guiapp "filees/internal/gui/app"
)

func TestProjectWailsTrayTracksConnectionRepositoriesAndLocks(t *testing.T) {
	projection := projectWailsTray(Snapshot{
		Connected: true, IconState: string(guiapp.IconActive),
		Servers:      []ServerProjection{{ID: "server", ReservationsKnown: true}},
		Repositories: []RepoProjection{{ID: "one"}, {ID: "two"}},
		Reservations: []ReservationProjection{{ID: "lock"}},
	})
	if projection.Icon != guiapp.IconActive || projection.Status != "Połączono · 2 repo · 1 blokada" || projection.Tooltip == "" {
		t.Fatalf("projection = %+v", projection)
	}

	disconnected := projectWailsTray(Snapshot{})
	if disconnected.Icon != guiapp.IconDisconnected || disconnected.Status != "Rozłączono · 0 repo · 0 blokad" {
		t.Fatalf("disconnected = %+v", disconnected)
	}

	unknown := projectWailsTray(Snapshot{Connected: true, Servers: []ServerProjection{{ID: "server"}}})
	if unknown.Status != "Połączono · 0 repo · ? blokad" {
		t.Fatalf("unknown reservations = %+v", unknown)
	}
}
