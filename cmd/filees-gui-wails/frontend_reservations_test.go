package main

import (
	"strings"
	"testing"
)

func TestFrontendKeepsHealthyReservationRowsWhenAnotherServerIsUnavailable(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	for _, wanted := range []string{
		"function reservationProjectionState(snapshot)",
		"!server.reservations_known || server.reservation_projection === \"unknown\"",
		"server.reservation_projection === \"offline\"",
		"server.reservation_projection === \"stale\"",
		"Częściowa lista — brak aktualnej emisji:",
		"Lokalne lustro — tor stanowy offline:",
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
