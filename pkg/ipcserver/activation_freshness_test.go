package ipcserver

import (
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestFreshnessCannotRestoreStartupRealmRightsOrReadiness(t *testing.T) {
	s := New("")
	s.RegisterActivation(contract.ActivationStatus{ServerID: "lab"})
	current := contract.ActivationStatus{
		ServerID: "lab", DisplayName: "Lab", RealmID: "reader", RealmAlias: "odbiorca",
		ClientID: "client", ClientRole: "normal", CanCreateRepositories: true,
		RepositoriesReady: false, PendingRequiredRepos: 1, SessionTimeoutMin: 45,
	}
	s.RegisterActivation(current)
	observed := contract.ActivationStatus{ServerID: "lab", ViewGeneration: 3,
		ViewSyncedAt: "2026-09-05T13:00:00Z", ViewSyncError: "offline", ViewSyncFailures: 1}
	s.SetActivationFreshness(observed)
	got := s.activations["lab"]
	if got.RealmID != current.RealmID || got.RealmAlias != current.RealmAlias ||
		got.ClientID != current.ClientID || got.DisplayName != current.DisplayName ||
		got.ClientRole != current.ClientRole || !got.CanCreateRepositories ||
		got.RepositoriesReady || got.PendingRequiredRepos != 1 || got.SessionTimeoutMin != 45 {
		t.Fatalf("freshness replaced canonical activation facts: %+v", got)
	}
	if got.ViewGeneration != 3 || got.ViewSyncError != "offline" || got.ViewSyncFailures != 1 {
		t.Fatalf("freshness not applied: %+v", got)
	}
	s.RemoveServer("lab")
	s.SetActivationFreshness(observed)
	if _, exists := s.activations["lab"]; exists {
		t.Fatal("late monitor callback resurrected removed profile")
	}
}
