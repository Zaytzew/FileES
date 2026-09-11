package main

import (
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/journal"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestIntentAlertFollowsDaemonPendingEvenWhenStale(t *testing.T) {
	vm := guiapp.ViewModel{Connected: true, Repos: []guiapp.RepoViewModel{{ID: "docs", Attached: true, Pending: contract.PendingStats{RenameUncertain: 1}}}}
	for _, stale := range []bool{false, true} {
		vm.Stale = stale
		if !projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
			t.Fatal("decision disappeared without resolution")
		}
	}
	vm.Repos[0].Pending.RenameUncertain = 0
	if projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
		t.Fatal("resolved decision remains")
	}
	vm.Repos[0].Pending.RenameUncertain = 1
	vm.Repos[0].ServerDeleted = true
	if projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
		t.Fatal("deleted repository asks for publication")
	}
	vm.Repos[0].ServerDeleted = false
	vm.Repos[0].Attached = false
	if projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
		t.Fatal("remote repository asks for local resolution")
	}
}

func TestIntentResolutionFolderActionProjection(t *testing.T) {
	request := platform.SettingsDialogRequest{FocusRepoID: "docs", Servers: []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{ID: "docs", CanResolveIntents: true}}}}}
	projection, ok := projectRepositorySettings(request)
	if !ok || len(projection.Actions) != 1 || projection.Actions[0].ID != string(platform.SettingsDialogResolveIntents) {
		t.Fatalf("projection=%+v", projection)
	}
	request.Servers[0].Folders[0].CanResolveIntents = false
	projection, ok = projectRepositorySettings(request)
	if !ok || len(projection.Actions) != 0 {
		t.Fatal("renderer invented permission")
	}
}
