package notifications

import (
	"testing"
	"time"

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
	if got := policy.Observe(disconnected); len(got) != 0 {
		t.Fatalf("disconnect notifications = %#v", got)
	}
	attention := baseline
	attention.Repos = []app.RepoViewModel{{ID: "repo", State: contract.StateDegraded}}
	attention.Errors = []app.ErrorViewModel{{ID: "e1", Code: "LOCK-2001", Severity: "ERROR", Message: "locked"}}
	got := policy.Observe(attention)
	if len(got) != 2 || got[0].ID != "repo.attention.repo" || got[1].ID != "daemon.error.e1" {
		t.Fatalf("recovery notifications = %#v", got)
	}
	if got := policy.Observe(attention); len(got) != 0 {
		t.Fatalf("duplicate notifications = %#v", got)
	}
}

func TestPolicySuppressesConnectionToastUntilFirstSuccessfulStartupHandshake(t *testing.T) {
	var policy Policy
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC)
	policy.SetClock(func() time.Time { return now })
	disconnected := app.ViewModel{}
	if got := policy.Observe(disconnected); len(got) != 0 {
		t.Fatalf("startup baseline notifications = %#v", got)
	}
	connected := disconnected
	connected.Connected = true
	if got := policy.Observe(connected); len(got) != 0 {
		t.Fatalf("startup recovery notifications = %#v", got)
	}
	disconnectedAgain := connected
	disconnectedAgain.Connected = false
	if got := policy.Observe(disconnectedAgain); len(got) != 0 {
		t.Fatalf("post-startup disconnect notifications = %#v", got)
	}
	now = now.Add(30 * time.Second)
	if got := policy.Observe(disconnectedAgain); len(got) != 0 {
		t.Fatalf("transient disconnect notifications = %#v", got)
	}
	if got := policy.Observe(connected); len(got) != 0 {
		t.Fatalf("post-startup reconnect notifications = %#v", got)
	}

	policy.Observe(disconnectedAgain)
	now = now.Add(ConnectionGrace + time.Second)
	if got := policy.Observe(disconnectedAgain); len(got) != 1 || got[0].ID != "daemon.disconnected" {
		t.Fatalf("sustained disconnect notifications = %#v", got)
	}
	if got := policy.Observe(connected); len(got) != 1 || got[0].ID != "daemon.connected" {
		t.Fatalf("sustained reconnect notifications = %#v", got)
	}
}

func TestPolicySuppressesExpectedRestartDisconnect(t *testing.T) {
	var policy Policy
	connected := app.ViewModel{Connected: true}
	policy.Observe(connected)
	policy.SuppressConnectionTransitions()
	disconnected := app.ViewModel{}
	if got := policy.Observe(disconnected); len(got) != 0 {
		t.Fatalf("expected-restart notifications = %#v", got)
	}
	policy.RestoreConnectionTransitions()
	if got := policy.Observe(connected); len(got) != 0 {
		t.Fatalf("restored notifications = %#v", got)
	}
}

func TestPolicyDoesNotReplayCachedErrorsDuringStartupRefresh(t *testing.T) {
	var policy Policy
	connecting := app.ViewModel{Connected: true, Stale: true}
	if got := policy.Observe(connecting); len(got) != 0 {
		t.Fatalf("connecting notifications = %#v", got)
	}
	fresh := app.ViewModel{Connected: true, Errors: []app.ErrorViewModel{{ID: "cached", Code: "NET-4007", Severity: "WARN", Message: "network unreachable"}}}
	if got := policy.Observe(fresh); len(got) != 0 {
		t.Fatalf("cached startup notifications = %#v", got)
	}
	newError := fresh
	newError.Errors = append(newError.Errors, app.ErrorViewModel{ID: "new", Code: "NET-4008", Severity: "ERROR", Message: "new failure"})
	if got := policy.Observe(newError); len(got) != 1 || got[0].ID != "daemon.error.new" {
		t.Fatalf("new error notifications = %#v", got)
	}
}
