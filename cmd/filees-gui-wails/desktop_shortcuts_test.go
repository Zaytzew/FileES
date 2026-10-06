package main

import (
	"testing"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/journal"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

func TestShelfShortcutProjectionMatchesAdmission(t *testing.T) {
	for _, variant := range []string{"owned", "wrong-server", "guest", "deleted-parent", "stale", "unsupported", "ordinary", "no-parent"} {
		t.Run(variant, func(t *testing.T) {
			caps := map[string]bool{}
			for _, cap := range []string{contract.CapRepoUploadChannelList, contract.CapRepoUploadChannelCreate, contract.CapRepoUploadChannelUpdate, contract.CapRepoUploadChannelRevoke, contract.CapRepoUploadChannelDelete, contract.CapRepoShelfList} {
				caps[cap] = true
			}
			vm := guiapp.ViewModel{Connected: true, Capabilities: caps, Servers: []guiapp.ServerViewModel{{ID: "s", RealmID: "owner", CanCreateRepositories: true}}, Repos: []guiapp.RepoViewModel{
				{ID: "parent", ServerID: "s", OwnerRealmID: "owner"},
				{ID: "shelf", ServerID: "s", Purpose: "upload_shelf", ParentRepoID: "parent"},
			}}
			serverID := "s"
			switch variant {
			case "wrong-server":
				serverID = "another"
			case "guest":
				vm.Repos[0].OwnerRealmID = "guest"
			case "deleted-parent":
				vm.Repos[0].ServerDeleted = true
			case "stale":
				vm.Stale = true
			case "unsupported":
				delete(caps, contract.CapRepoShelfList)
			case "ordinary":
				vm.Repos[1].Purpose = ""
			case "no-parent":
				vm.Repos[1].ParentRepoID = "missing"
			}
			intent, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentBrowseShelf), ServerID: serverID, RepoID: "shelf"})
			if allowed != (variant == "owned") {
				t.Fatal(allowed)
			}
			projection := projectViewModel(vm, journal.Texts{})
			wantVisible := variant == "owned" || variant == "wrong-server"
			if projection.Repositories[1].CanBrowseShelf != wantVisible {
				t.Fatal("projection/admission mismatch")
			}
			if allowed && (intent.ServerID != "s" || intent.RepoID != "shelf" || intent.Kind != tray.IntentBrowseShelf) {
				t.Fatal(intent)
			}
		})
	}
}
