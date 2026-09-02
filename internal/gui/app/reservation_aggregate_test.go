package app

import (
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

func src(state contract.ReservationSourceState, asOf time.Time) contract.ReservationSource {
	return contract.ReservationSource{State: state, AsOf: asOf}
}

// The coordinator queries only repositories in state "active", so every
// inactive one reports "unknown" forever. Letting a single unknown decide the
// whole server marked both live servers as having no current emission while
// emissions were arriving - permanently, on any real installation.
func TestOneUnknownSourceDoesNotHideAWorkingEmission(t *testing.T) {
	now := time.Now().UTC()
	state, asOf := aggregateReservationProjection([]contract.ReservationSource{
		src(contract.ReservationSourceUnknown, time.Time{}),
		src(contract.ReservationSourceFresh, now),
		src(contract.ReservationSourceUnknown, time.Time{}),
	}, true)

	if state != string(contract.ReservationSourceFresh) {
		t.Fatalf("state = %q, want fresh: one unanswered repository must not mask a live emission", state)
	}
	if asOf == "" {
		t.Fatal("an answered source must still contribute its as-of stamp")
	}
}

// The warning must survive where it is true: nothing answered at all.
func TestServerWithNoAnsweredSourceStaysUnknown(t *testing.T) {
	state, asOf := aggregateReservationProjection([]contract.ReservationSource{
		src(contract.ReservationSourceUnknown, time.Time{}),
		src(contract.ReservationSourceUnknown, time.Time{}),
	}, true)
	if state != string(contract.ReservationSourceUnknown) || asOf != "" {
		t.Fatalf("state=%q asOf=%q, want unknown with no stamp", state, asOf)
	}
	if state, _ := aggregateReservationProjection(nil, true); state != string(contract.ReservationSourceUnknown) {
		t.Fatalf("no sources at all = %q, want unknown", state)
	}
	if state, _ := aggregateReservationProjection([]contract.ReservationSource{src(contract.ReservationSourceFresh, time.Now())}, false); state != string(contract.ReservationSourceUnknown) {
		t.Fatal("an unknown server must stay unknown regardless of its sources")
	}
}

// Among sources that did answer, the worst one still decides.
func TestWorstAnsweredSourceDecides(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name    string
		sources []contract.ReservationSource
		want    contract.ReservationSourceState
	}{
		{"stale beats fresh", []contract.ReservationSource{src(contract.ReservationSourceFresh, now), src(contract.ReservationSourceStale, now)}, contract.ReservationSourceStale},
		{"offline beats stale", []contract.ReservationSource{src(contract.ReservationSourceStale, now), src(contract.ReservationSourceOffline, now)}, contract.ReservationSourceOffline},
		{"offline beats stale despite unknown", []contract.ReservationSource{src(contract.ReservationSourceUnknown, time.Time{}), src(contract.ReservationSourceOffline, now), src(contract.ReservationSourceFresh, now)}, contract.ReservationSourceOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if state, _ := aggregateReservationProjection(tc.sources, true); state != string(tc.want) {
				t.Fatalf("state = %q, want %q", state, tc.want)
			}
		})
	}
}
