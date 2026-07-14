package notifications

import (
	"testing"

	"filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
)

func TestPolicyConnectionAttentionAndNewErrorTransitions(t *testing.T) {
	var policy Policy
	baseline := app.ViewModel{Connected: true, Repos: []app.RepoViewModel{{ID: "repo", State: contract.StateActive}}}
	if got := policy.Observe(baseline); len(got) != 0 {
		t.Fatalf("baseline notifications = %#v", got)
	}
	disconnected := baseline
	disconnected.Connected = false
	if got := policy.Observe(disconnected); len(got) != 1 || got[0].ID != "daemon.disconnected" {
		t.Fatalf("disconnect notifications = %#v", got)
	}
	attention := baseline
	attention.Repos = []app.RepoViewModel{{ID: "repo", State: contract.StateDegraded}}
	attention.Errors = []app.ErrorViewModel{{ID: "e1", Code: "LOCK-2001", Severity: "ERROR", Message: "locked"}}
	got := policy.Observe(attention)
	if len(got) != 3 || got[0].ID != "daemon.connected" || got[1].ID != "repo.attention.repo" || got[2].ID != "daemon.error.e1" {
		t.Fatalf("recovery notifications = %#v", got)
	}
	if got := policy.Observe(attention); len(got) != 0 {
		t.Fatalf("duplicate notifications = %#v", got)
	}
}
