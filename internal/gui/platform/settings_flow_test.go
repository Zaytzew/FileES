package platform

import "testing"

func TestBuildSettingsWizardListsServerActionsWithoutPickingAFolder(t *testing.T) {
	wizard := BuildSettingsWizard(SettingsDialogRequest{Servers: []SettingsServer{{
		ID: "office", Name: "Biuro", CanSetSessionTimeout: true, CanSetRealmVisibility: true, CanAddFolder: true,
		Folders: []SettingsFolder{{ID: "docs", Name: "Dokumenty", CanManageGrants: true}},
	}}})
	if len(wizard.Servers) != 1 {
		t.Fatalf("servers=%d", len(wizard.Servers))
	}
	ids := actionIDs(wizard.Servers[0])
	for _, want := range []string{"session_timeout", "realm_visibility", "add_folder", "manage_grants", "detach_server", "remove_realm"} {
		if !containsString(ids, want) {
			t.Errorf("missing %s in %#v", want, ids)
		}
	}
	if containsString(ids, "connect_repositories") || containsString(ids, "detach_folder") {
		t.Fatalf("ineligible folder actions leaked: %#v", ids)
	}
	grants := mustAction(t, wizard.Servers[0], "manage_grants")
	if !grants.NeedsFolder || len(grants.Folders) != 1 || grants.Folders[0].ID != "docs" {
		t.Fatalf("grants=%#v", grants)
	}
}

func TestBuildSettingsWizardOffersUploadChannelsWhenEligible(t *testing.T) {
	wizard := BuildSettingsWizard(SettingsDialogRequest{Servers: []SettingsServer{{
		ID: "office", Name: "Biuro",
		Folders: []SettingsFolder{{ID: "docs", Name: "Dokumenty", CanManageUploadChannels: true}},
	}}})
	action := mustAction(t, wizard.Servers[0], "upload_channels")
	if action.Label != "Półki przyjęcia" || !action.NeedsFolder || len(action.Folders) != 1 || action.Folders[0].ID != "docs" {
		t.Fatalf("upload_channels=%#v", action)
	}
}

func TestBuildSettingsWizardHidesFolderActionWhenNoShareIsEligible(t *testing.T) {
	wizard := BuildSettingsWizard(SettingsDialogRequest{Servers: []SettingsServer{{
		ID: "audit", CanSetSessionTimeout: true,
		Folders: []SettingsFolder{{ID: "foreign", CanManageGrants: false, CanDetach: false}},
	}}})
	ids := actionIDs(wizard.Servers[0])
	if containsString(ids, "manage_grants") || containsString(ids, "detach_folder") || containsString(ids, "add_folder") {
		t.Fatalf("offered ineligible actions: %#v", ids)
	}
	if !containsString(ids, "session_timeout") || !containsString(ids, "detach_server") {
		t.Fatalf("local/server actions missing: %#v", ids)
	}
}

func TestBuildSettingsWizardConnectListsOnlyConnectableShares(t *testing.T) {
	wizard := BuildSettingsWizard(SettingsDialogRequest{Servers: []SettingsServer{{
		ID: "office",
		Folders: []SettingsFolder{
			{ID: "docs", Name: "Dokumenty", CanConnect: false, CanDetach: true},
			{ID: "remote", Name: "Zdalne", CanConnect: true},
		},
	}}})
	connect := mustAction(t, wizard.Servers[0], "connect_repositories")
	if !connect.Multi || len(connect.Folders) != 1 || connect.Folders[0].ID != "remote" {
		t.Fatalf("connect=%#v", connect)
	}
}

func actionIDs(server SettingsWizardServer) []string {
	ids := make([]string, 0, len(server.Actions))
	for _, action := range server.Actions {
		ids = append(ids, action.ID)
	}
	return ids
}

func mustAction(t *testing.T, server SettingsWizardServer, id string) SettingsWizardAction {
	t.Helper()
	action, ok := findWizardAction(server, id)
	if !ok {
		t.Fatalf("action %s missing from %#v", id, actionIDs(server))
	}
	return action
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
