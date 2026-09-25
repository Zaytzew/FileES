package main

import (
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

// Another realm sharing a repository used to be silent on the recipient's
// side (owner's live acceptance, 2026-09-25). The first fresh snapshot is the
// baseline; later a new guest repository and a changed access are announced.
func TestGrantAlertPolicyAnnouncesNewGuestRepositoriesAndAccessChanges(t *testing.T) {
	language, err := loadNativeLanguage()
	if err != nil {
		t.Fatal(err)
	}
	language.selectLocale("pl")
	servers := []ServerProjection{{ID: "office", DisplayName: "Biuro"}}
	snapshot := func(repos ...RepoProjection) Snapshot {
		return Snapshot{Connected: true, Servers: servers, Repositories: repos}
	}
	old := RepoProjection{ID: "old", ServerID: "office", DisplayName: "Stare", Ownership: "guest", Access: contract.AccessReadOnly}
	own := RepoProjection{ID: "own", ServerID: "office", DisplayName: "Moje", Ownership: "owned", Access: contract.AccessReadWrite}
	var policy grantAlertPolicy
	if got := policy.Observe(snapshot(old, own), language); len(got) != 0 {
		t.Fatalf("the baseline announced grants that were already there: %+v", got)
	}

	plans := RepoProjection{ID: "plans", ServerID: "office", DisplayName: "Rysunki", Ownership: "guest", Access: contract.AccessReadWrite}
	shelf := RepoProjection{ID: "shelf", ServerID: "office", DisplayName: "Półka", Ownership: "guest", Purpose: "upload_shelf", Access: contract.AccessReadOnly}
	got := policy.Observe(snapshot(old, own, plans, shelf), language)
	if len(got) != 1 || got[0].Title != "Udostępniono Ci repozytorium" || !strings.Contains(got[0].Body, "„Rysunki”") || !strings.Contains(got[0].Body, "Biuro") || !strings.Contains(got[0].Body, "odczyt i zapis") {
		t.Fatalf("new grant: %+v", got)
	}
	if got := policy.Observe(snapshot(old, own, plans, shelf), language); len(got) != 0 {
		t.Fatalf("an unchanged grant was announced again: %+v", got)
	}

	old.Access = contract.AccessReadWrite
	got = policy.Observe(snapshot(old, own, plans), language)
	if len(got) != 1 || got[0].Title != "Zmieniono Twój dostęp" || !strings.Contains(got[0].Body, "„Stare”") {
		t.Fatalf("access change: %+v", got)
	}

	if got := policy.Observe(Snapshot{Connected: true, Stale: true}, language); len(got) != 0 {
		t.Fatalf("a stale snapshot produced notifications: %+v", got)
	}
}
