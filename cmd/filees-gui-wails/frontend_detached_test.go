package main

import (
	"strings"
	"testing"
	"time"

	guiapp "filees/internal/gui/app"
)

func detachedSnapshot(t *testing.T, detachments []guiapp.DetachmentViewModel) Snapshot {
	t.Helper()
	vm := guiapp.ViewModel{Connected: true, Detachments: detachments}
	return projectViewModelAt(vm, time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC))
}

func TestTheFrontendGetsEveryJudgementAlreadyMade(t *testing.T) {
	snapshot := detachedSnapshot(t, []guiapp.DetachmentViewModel{{
		ServerID: "manual", DisplayName: "manual", Cause: "revoked", At: "2026-09-03T18:10:00Z",
		WorkingCopies: []string{`C:\Projekty\Willa`},
	}})
	if len(snapshot.Detachments) != 1 {
		t.Fatalf("detachments = %d, want 1", len(snapshot.Detachments))
	}
	got := snapshot.Detachments[0]
	// The panel is a presenter and a collector of intentions. Each of these is
	// a decision it cannot make or check for itself, so each has to arrive
	// finished: the wording, the time as a reader reads it, and whether
	// anything is left to do.
	if got.Summary == "" || got.RelativeTime == "" || got.ExactTime == "" {
		t.Fatalf("projection = %+v; the panel cannot compute any of these", got)
	}
	if !got.NeedsReactivation {
		t.Error("a revoked client needs re-activation and the panel has no way to work that out")
	}
	if !strings.Contains(got.Summary, "manual") {
		t.Errorf("summary = %q", got.Summary)
	}
	if len(got.WorkingCopies) != 1 {
		t.Errorf("working copies = %v; the files stayed on disk and this is where", got.WorkingCopies)
	}
}

func TestASelfDetachmentOffersNothingToDo(t *testing.T) {
	snapshot := detachedSnapshot(t, []guiapp.DetachmentViewModel{{
		ServerID: "manual", DisplayName: "manual", Cause: "self", At: "2026-09-03T17:40:00Z",
	}})
	if len(snapshot.Detachments) != 1 {
		t.Fatalf("detachments = %d, want 1", len(snapshot.Detachments))
	}
	if snapshot.Detachments[0].NeedsReactivation {
		t.Error("a deliberate detachment is finished business; offering re-activation invites undoing a confirmed decision")
	}
}

func TestAnEmptyDetachmentListStaysAnArrayForTheFrontend(t *testing.T) {
	snapshot := detachedSnapshot(t, nil)
	if snapshot.Detachments == nil {
		t.Fatal("Detachments is nil; the frontend reads .length on it")
	}
	if len(snapshot.Detachments) != 0 {
		t.Fatalf("detachments = %d, want 0", len(snapshot.Detachments))
	}
}

// The lifetime belongs to the daemon. The projection passes records straight
// through, so an expired row can only be absent because the daemon dropped it -
// never because two layers each held an opinion about time and happened to
// agree.
func TestTheProjectionAppliesNoLifetimeOfItsOwn(t *testing.T) {
	old := guiapp.ViewModel{Connected: true, Detachments: []guiapp.DetachmentViewModel{{
		ServerID: "manual", DisplayName: "manual", Cause: "self", At: "2026-08-01T09:00:00Z",
	}}}
	snapshot := projectViewModelAt(old, time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC))
	if len(snapshot.Detachments) != 1 {
		t.Fatalf("detachments = %d, want 1: expiry is the daemon's job, not this layer's", len(snapshot.Detachments))
	}
}

// The panel describes how things stand, so a server that is back does not
// belong in a list of endings - the reader would have to decide which of two
// adjacent statements to believe. The journal keeps the entry; that is the
// other half of the same rule and is asserted in internal/gui/journal.
func TestAReattachedServerLeavesThePanelButNotTheRecord(t *testing.T) {
	snapshot := detachedSnapshot(t, []guiapp.DetachmentViewModel{
		{ServerID: "manual", DisplayName: "manual", Cause: "revoked",
			At: "2026-09-03T18:10:00Z", ReattachedAt: "2026-09-03T19:30:00Z"},
		{ServerID: "spot", DisplayName: "spot", Cause: "self", At: "2026-09-03T17:40:00Z"},
	})
	if len(snapshot.Detachments) != 1 {
		t.Fatalf("detachments = %d, want 1", len(snapshot.Detachments))
	}
	if snapshot.Detachments[0].ServerID != "spot" {
		t.Fatalf("panel shows %q; a server that is back belongs in the projection", snapshot.Detachments[0].ServerID)
	}
}

func TestDetachedPanelIsPresentAndStartsCollapsed(t *testing.T) {
	index := embeddedFrontendFile(t, "frontend/index.html")
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")

	for _, required := range []string{`id="detached-card"`, `id="detached-body"`, `id="detached"`, `Ostatnio odłączone`} {
		if !strings.Contains(index, required) {
			t.Fatalf("frontend index does not contain %q", required)
		}
	}
	// Collapsed by default, and the only card that is. Its body carries the
	// name of a server somebody deliberately cut ties with next to the paths
	// of its folders - which is the exposure r790 removed from the projection.
	// A heading and a count expose nothing; the names wait for a click.
	if !strings.Contains(index, `data-toggle-card="detached-body" aria-expanded="false"`) {
		t.Fatal("the detached card does not start collapsed")
	}
	if !strings.Contains(index, `<div id="detached-body" class="side-card-body" hidden>`) {
		t.Fatal("the detached card body is not hidden on load")
	}
	for _, required := range []string{`renderDetached(snapshot)`, `snapshot.detachments`, `item.needs_reactivation`, `item.working_copies`} {
		if !strings.Contains(script, required) {
			t.Fatalf("frontend script does not contain %q", required)
		}
	}
	for _, required := range []string{".detached-list", ".detached-row", ".detached-paths"} {
		if !strings.Contains(styles, required) {
			t.Fatalf("frontend styles do not contain %q", required)
		}
	}
}

// The panel must not hold knowledge of its own. Anything resembling a lifetime
// computed in the frontend is the failure this design exists to avoid: a panel
// that cannot ask will answer anyway, and the answer will be wrong.
func TestTheDetachedPanelComputesNothing(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	start := strings.Index(script, "function renderDetached(")
	if start < 0 {
		t.Fatal("renderDetached is missing")
	}
	end := strings.Index(script[start:], "\nfunction ")
	body := script[start:]
	if end > 0 {
		body = script[start : start+end]
	}
	for _, forbidden := range []string{"Date.now", "new Date", "localStorage", "sessionStorage", "setTimeout", "setInterval"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("renderDetached uses %q; the panel presents and collects intent, it does not decide", forbidden)
		}
	}
}
