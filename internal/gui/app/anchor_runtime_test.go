package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestAnchorRuntimeDoesNotMasqueradeAsOnlineReady(t *testing.T) {
	r := RepoViewModel{Attached: true, State: contract.StateActive, Connectivity: contract.ConnOnline}
	vm := ViewModel{Connected: true}
	for _, phase := range []string{"preparing", "connecting", "failed", "ready", "future"} {
		r.AnchorRuntime = &contract.AnchorRuntimeStatus{Phase: phase}
		want, icon := RepoDisplayBusy, IconBusy
		if phase == "failed" || phase == "future" {
			want, icon = RepoDisplayAttention, IconError
		}
		if phase == "ready" {
			want, icon = RepoDisplayActive, IconActive
		}
		if r.DisplayState() != want || repoIconState(r) != icon {
			t.Fatalf("%s: %s %s", phase, r.DisplayState(), repoIconState(r))
		}
		if vm.CanFoldInactive(r) != (phase == "ready") {
			t.Fatalf("attention hidden in idle group: %s", phase)
		}
	}
	r.Attached = false
	r.AnchorRuntime.Phase = "failed"
	if r.DisplayState() != RepoDisplayActive {
		t.Fatal("detached source applied")
	}
}

func TestAnchorPreparationCannotHideRequiredUserAction(t *testing.T) {
	for _, phase := range []string{"preparing", "connecting"} {
		for _, reason := range []string{"conflict", "commit-recovery", "rename-uncertain", "degraded"} {
			r := RepoViewModel{Attached: true, State: contract.StateActive, Connectivity: contract.ConnOnline,
				AnchorRuntime: &contract.AnchorRuntimeStatus{Phase: phase}}
			switch reason {
			case "conflict":
				r.Conflicts = 1
			case "commit-recovery":
				r.CommitRecoveryRequired = true
			case "rename-uncertain":
				r.Pending.RenameUncertain = 1
			case "degraded":
				r.State = contract.StateDegraded
			}
			if r.DisplayState() != RepoDisplayAttention || repoIconState(r) != IconError {
				t.Fatalf("%s hid %s", phase, reason)
			}
		}
	}
}
