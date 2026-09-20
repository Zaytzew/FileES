package main

import (
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestRejectedActionExplainsCurrentReasonWithoutQueueing(t *testing.T) {
	base := guiapp.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoPublish: true, contract.CapRepoAttachIntent: true, contract.CapRepoAttachApprove: true}, Repos: []guiapp.RepoViewModel{{ID: "repo", ServerID: "server", State: contract.StateActive, Attached: true, LocalPath: "/wc", Access: contract.AccessReadWrite}}}
	for _, tc := range []struct {
		name   string
		kind   tray.IntentKind
		mutate func(*guiapp.ViewModel)
		reason string
	}{
		{"offline", tray.IntentPublish, func(v *guiapp.ViewModel) { v.Connected = false }, "action_disconnected"},
		{"stale", tray.IntentPublish, func(v *guiapp.ViewModel) { v.Stale = true }, "action_stale"},
		{"missing", tray.IntentPublish, func(v *guiapp.ViewModel) { v.Repos = nil }, "action_repository_missing"},
		{"read-only", tray.IntentPublish, func(v *guiapp.ViewModel) { v.Repos[0].Access = contract.AccessReadOnly }, "action_read_only"},
		{"unattached", tray.IntentPublish, func(v *guiapp.ViewModel) { v.Repos[0].Attached = false }, "action_not_attached"},
		{"initializing", tray.IntentAttachRepository, func(v *guiapp.ViewModel) { v.Repos[0].Attached = false; v.Repos[0].State = contract.StateInitializing }, "action_repository_initializing"},
		{"capability", tray.IntentPublish, func(v *guiapp.ViewModel) { v.Capabilities = nil }, "action_capability_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm := base
			vm.Repos = append([]guiapp.RepoViewModel(nil), base.Repos...)
			tc.mutate(&vm)
			actions := make(chan tray.Intent, 1)
			service := &GUIService{view: vm, actions: actions}
			got := service.Trigger(ActionRequest{Kind: string(tc.kind), RepoID: "repo", ServerID: "server"})
			if got.Accepted || got.Code != "action_unavailable" || got.Reason != tc.reason || len(actions) != 0 {
				t.Fatalf("wrong rejection: %+v", got)
			}
		})
	}
}
