package main

import (
	"strings"
	"testing"
)

func TestFrontendKeepsHealthyReservationRowsWhenAnotherServerIsUnavailable(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	for _, wanted := range []string{
		"function reservationProjectionState(snapshot)",
		"servers.filter((server) => !server.reservations_known)",
		"Częściowa lista — brak aktualnej emisji:",
		"availabilityHTML + requestsHTML + reservationsHTML",
		"ostatni znany stan · demon offline",
	} {
		if !strings.Contains(script, wanted) {
			t.Fatalf("frontend reservation projection missing %q", wanted)
		}
	}
	for _, forbidden := range []string{
		"const reservationsKnown = !(snapshot.connected",
		"reservations = nil",
		"Lista blokad jest chwilowo niedostępna.",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("global reservation blackout remains in frontend: %q", forbidden)
		}
	}
}
