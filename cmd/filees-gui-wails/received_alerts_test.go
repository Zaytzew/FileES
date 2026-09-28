package main

import (
	"testing"
)

// Owner, 2026-09-28: what background updates bring in should reach the tray,
// once per repository and revision, never replayed after a reconnect.
func TestReceivedAlertPolicyAnnouncesEachIncomingUpdateOnce(t *testing.T) {
	language, err := loadNativeLanguage()
	if err != nil {
		t.Fatal(err)
	}
	language.selectLocale("pl")
	repos := []RepoProjection{{ID: "r1", DisplayName: "KRAŃCOWA"}}
	old := ActivityProjection{RepoID: "r1", Path: "old.dwg", Stage: "received", Revision: 5}
	baseline := Snapshot{Connected: true, Repositories: repos, Activity: []ActivityProjection{old}}
	var policy receivedAlertPolicy
	if got := policy.Observe(baseline, language); len(got) != 0 {
		t.Fatalf("existing receipts announced at startup: %+v", got)
	}

	incoming := baseline
	incoming.Activity = []ActivityProjection{
		{RepoID: "r1", Path: "01/a.dwg", Stage: "received", Revision: 9},
		{RepoID: "r1", Path: "01/b.pdf", Stage: "received", Revision: 9},
		{RepoID: "r1", Path: "c.txt", Stage: "received", Revision: 9},
		{RepoID: "r1", Path: "d.txt", Stage: "received", Revision: 9},
		{RepoID: "r1", Path: "mine.dwg", Stage: "published", Revision: 8},
		old,
	}
	got := policy.Observe(incoming, language)
	if len(got) != 1 || got[0].ID != "activity.received.r1:9" {
		t.Fatalf("notifications = %+v", got)
	}
	if got[0].Title != "FileES — pobrano zmiany z serwera" || got[0].Body != "KRAŃCOWA · r9 — a.dwg, b.pdf, c.txt i 1 więcej" {
		t.Fatalf("title=%q body=%q", got[0].Title, got[0].Body)
	}

	// A reconnect re-delivers the same feed: nothing is replayed.
	policy.Observe(Snapshot{Connected: false}, language)
	if got := policy.Observe(incoming, language); len(got) != 0 {
		t.Fatalf("replayed after reconnect: %+v", got)
	}
	// A stale snapshot is not news.
	stale := incoming
	stale.Stale = true
	stale.Activity = append([]ActivityProjection{{RepoID: "r1", Path: "e.txt", Stage: "received", Revision: 10}}, incoming.Activity...)
	if got := policy.Observe(stale, language); len(got) != 0 {
		t.Fatalf("announced from a stale snapshot: %+v", got)
	}
	// Once fresh again, revision 10 is announced, with a single file name.
	stale.Stale = false
	if got := policy.Observe(stale, language); len(got) != 1 || got[0].Body != "KRAŃCOWA · r10 — e.txt" {
		t.Fatalf("revision 10 = %+v", got)
	}
}
