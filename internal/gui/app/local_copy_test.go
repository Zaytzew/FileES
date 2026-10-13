package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestLocalCopyOpenAndProvisioningAreIndependentOfServer(t *testing.T) {
	repo := RepoViewModel{LocalPath: "/local", State: contract.StateInitializing,
		LocalCopy: &contract.LocalCopySummary{Path: "/local", Kind: "virtual", Presence: "present", Lifecycle: "attached"}}
	for _, state := range []string{contract.StateInitializing, contract.StateOffline} {
		repo.State = state
		if !repo.CanOpenLocalFolder() || repo.LocallyProvisioning() || repo.Attached || repo.CanWrite() {
			t.Fatalf("local knowledge confused with import or permission: %+v", repo)
		}
	}
	repo.LocalCopy.Presence = "unavailable"
	if repo.CanOpenLocalFolder() {
		t.Fatal("unavailable folder opened")
	}
	repo.LocalCopy.Presence = "present"
	repo.LocalCopy.Path = "/old"
	if repo.CanOpenLocalFolder() {
		t.Fatal("moved snapshot opened mismatching root")
	}
	repo.LocalCopy = nil
	repo.Attached = true
	if !repo.CanOpenLocalFolder() {
		t.Fatal("older daemon attachment compatibility lost")
	}
}

func TestReducerKeepsLocalObservationIndependentOfOfflineStatus(t *testing.T) {
	state := newAppState().applyRepoList([]contract.RepoSummary{{ID: "repo", ServerID: "office", LocalPath: "/local",
		LocalCopy: &contract.LocalCopySummary{Path: "/local", Presence: "present", Kind: "sparse", Lifecycle: "attached"}}})
	state.snapshots["repo"] = contract.RepoStatus{RepoID: "repo", ServerID: "office", State: contract.StateOffline, Connectivity: contract.ConnOffline}
	repo := state.viewModel().Repos[0]
	if repo.LocalCopy == nil || !repo.CanOpenLocalFolder() || repo.Attached || repo.LocalCopy.Kind != "sparse" || repo.DisplayState() != RepoDisplayOffline {
		t.Fatalf("local presence lost or server availability fabricated: %+v", repo)
	}
}
