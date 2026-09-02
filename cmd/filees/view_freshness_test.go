package main

import (
	"errors"
	"testing"
	"time"

	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
)

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// A refused sync must not erase the view already held. The point of recording
// the failure is to say how old that view now is, not to throw it away: on
// 2026-09-02 the `manual` server had been refusing syncs for ten days while
// the desktop still had a usable projection from 23 August, and hiding it
// would have been worse than showing it stale.
func TestFailureKeepsTheViewAndCountsAgainstIt(t *testing.T) {
	generated := time.Date(2026, 8, 23, 17, 38, 6, 0, time.UTC)
	synced := time.Date(2026, 8, 23, 17, 38, 7, 0, time.UTC)
	f := newViewFreshness(fixedClock(synced))

	f.Synced("manual", clientview.View{Generation: 14, GeneratedAt: generated})
	f.Failed("manual", errors.New("svn: E170013: unable to connect"))
	f.Failed("manual", errors.New("svn: E170013: unable to connect"))

	status := f.Apply(contract.ActivationStatus{ServerID: "manual"})
	if status.ViewGeneration != 14 {
		t.Fatalf("the view we still hold must survive a refusal, got generation %d", status.ViewGeneration)
	}
	if status.ViewGeneratedAt != generated.Format(time.RFC3339Nano) {
		t.Fatalf("view_generated_at = %q", status.ViewGeneratedAt)
	}
	if status.ViewSyncedAt != synced.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("view_synced_at = %q", status.ViewSyncedAt)
	}
	if status.ViewSyncFailures != 2 {
		t.Fatalf("consecutive failures = %d, want 2", status.ViewSyncFailures)
	}
	if status.ViewSyncError == "" {
		t.Fatal("the reason must reach the presentation; a warning line in a log is what left this invisible for ten days")
	}
}

// A view in hand is the strongest statement that the lane works, so it clears
// the error rather than leaving a stale complaint next to fresh data.
func TestASuccessfulSyncClearsTheFailure(t *testing.T) {
	first := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	f := newViewFreshness(fixedClock(first))
	f.Failed("spot", errors.New("temporary"))
	f.Synced("spot", clientview.View{Generation: 26, GeneratedAt: first})

	status := f.Apply(contract.ActivationStatus{ServerID: "spot"})
	if status.ViewSyncError != "" || status.ViewSyncFailures != 0 {
		t.Fatalf("a delivered view must clear the failure, got %q after %d", status.ViewSyncError, status.ViewSyncFailures)
	}
	if status.ViewGeneration != 26 {
		t.Fatalf("generation = %d", status.ViewGeneration)
	}
}

// Freshness is per server. One healthy server must not answer for a dead one -
// that collapse is exactly what M20 was, on a different surface.
func TestServersDoNotAnswerForEachOther(t *testing.T) {
	now := time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC)
	f := newViewFreshness(fixedClock(now))
	f.Synced("spot", clientview.View{Generation: 26, GeneratedAt: now})
	f.Failed("manual", errors.New("proof does not match"))

	spot := f.Apply(contract.ActivationStatus{ServerID: "spot"})
	manual := f.Apply(contract.ActivationStatus{ServerID: "manual"})
	if spot.ViewSyncError != "" {
		t.Fatalf("a failure on one server leaked onto another: %q", spot.ViewSyncError)
	}
	if manual.ViewSyncError == "" || manual.ViewSyncFailures != 1 {
		t.Fatalf("the failing server lost its own state: %+v", manual)
	}
}

// A server nobody has synced yet says nothing rather than claiming to be
// current, because zero fields are how a presentation layer can tell "not
// measured" from "measured and fine".
func TestUnknownServerLeavesTheRecordEmpty(t *testing.T) {
	f := newViewFreshness(fixedClock(time.Now()))
	status := f.Apply(contract.ActivationStatus{ServerID: "never-seen"})
	if status.ViewGeneration != 0 || status.ViewSyncedAt != "" || status.ViewSyncError != "" {
		t.Fatalf("an unmeasured server must not look measured: %+v", status)
	}
}

// What the server says about producing the view is a different fact from
// whether we could fetch it, and merging them would lose the only case neither
// can see alone. manual on 2026-09-02 fetched successfully every time and, by
// the server's own report, had not produced since 23 August.
func TestProductionIsRecordedApartFromFetching(t *testing.T) {
	fetched := time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)
	produced := time.Date(2026, 8, 23, 17, 38, 6, 0, time.UTC)
	f := newViewFreshness(fixedClock(fetched))

	f.Synced("manual", clientview.View{Generation: 14, GeneratedAt: produced})
	f.Produced("manual", 14, produced)

	status := f.Apply(contract.ActivationStatus{ServerID: "manual"})
	if status.ViewSyncError != "" || status.ViewSyncFailures != 0 {
		t.Fatalf("fetching was fine and must be reported as such: %+v", status)
	}
	if status.ViewSyncedAt != fetched.Format(time.RFC3339Nano) {
		t.Fatalf("view_synced_at = %q", status.ViewSyncedAt)
	}
	if status.ServerViewProducedAt != produced.Format(time.RFC3339Nano) {
		t.Fatalf("the server's own production time must survive: %q", status.ServerViewProducedAt)
	}
	if status.ServerViewGeneration != 14 {
		t.Fatalf("server view generation = %d", status.ServerViewGeneration)
	}
}

// A server that has never answered about production must not look like one
// that answered "never": absent stays absent, so a presentation layer can tell
// "not asked yet" from "asked and told".
func TestUnreportedProductionStaysAbsent(t *testing.T) {
	f := newViewFreshness(fixedClock(time.Now()))
	f.Synced("spot", clientview.View{Generation: 26, GeneratedAt: time.Now()})
	status := f.Apply(contract.ActivationStatus{ServerID: "spot"})
	if status.ServerViewProducedAt != "" || status.ServerViewGeneration != 0 {
		t.Fatalf("production nobody reported must not be invented: %+v", status)
	}
}
