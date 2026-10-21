package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestWorkingCopyRecoveryIsIndependentOfPendingAndHead(t *testing.T) {
	r := RepoViewModel{ID: "repo", Attached: true, State: contract.StateActive, Connectivity: contract.ConnOnline, LocalRev: 51, HeadRev: 51,
		WorkingCopyRecovery: &contract.WorkingCopyRecoveryStatus{Phase: "running"}}
	if r.DisplayState() != RepoDisplayBusy || repoIconState(r) != IconBusy {
		t.Fatal("empty upload queue hid recovery", r)
	}
	r.WorkingCopyRecovery.Phase = "blocked"
	if r.DisplayState() != RepoDisplayAttention || repoIconState(r) != IconError {
		t.Fatal("failure hidden")
	}
	r.WorkingCopyRecovery.Phase = "waiting"
	if r.DisplayState() != RepoDisplayBusy {
		t.Fatal("waiting mislabeled failure")
	}
	r.Connectivity = contract.ConnOffline
	if r.DisplayState() != RepoDisplayOffline || repoIconState(r) != IconOffline {
		t.Fatal("server outage hidden")
	}
	r.Connectivity = contract.ConnOnline
	r.WorkingCopyRecovery = nil
	if r.DisplayState() != RepoDisplayActive || repoIconState(r) != IconActive {
		t.Fatal("completed recovery retained overlay")
	}
}
