package actions_test

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type realmGrantCall struct {
	serverID, repoID, realmID, access string
	revoke                            bool
}

type fakeRealmGrantManager struct {
	recipients []actions.RealmGrantRecipient
	calls      chan realmGrantCall
}

func (fake *fakeRealmGrantManager) ListRecipients(context.Context, string) ([]actions.RealmGrantRecipient, error) {
	return fake.recipients, nil
}

func (fake *fakeRealmGrantManager) SetVisibility(_ context.Context, serverID, visibility string) error {
	fake.calls <- realmGrantCall{serverID: serverID, access: visibility}
	return nil
}

func (fake *fakeRealmGrantManager) Grant(_ context.Context, serverID, repoID, realmID, access string) error {
	fake.calls <- realmGrantCall{serverID: serverID, repoID: repoID, realmID: realmID, access: access}
	return nil
}

func (fake *fakeRealmGrantManager) Revoke(_ context.Context, serverID, repoID, realmID string) error {
	fake.calls <- realmGrantCall{serverID: serverID, repoID: repoID, realmID: realmID, revoke: true}
	return nil
}

func TestControllerSettingsGrantsWriteAccessToSelectedRealm(t *testing.T) {
	manager := &fakeRealmGrantManager{recipients: []actions.RealmGrantRecipient{{RealmID: "realm-2", Alias: "biuro"}}, calls: make(chan realmGrantCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			if len(request.Servers) != 1 || len(request.Servers[0].Folders) != 1 || !request.Servers[0].Folders[0].CanManageGrants {
				t.Fatalf("grant action not offered for owned repo: %+v", request)
			}
			return platform.SettingsDialogResult{Action: platform.SettingsDialogManageGrants, ServerID: "office", RepoID: "repo-1"}, nil
		},
		RealmGrantsFunc: func(_ context.Context, request platform.RealmGrantDialogRequest) (platform.RealmGrantDialogResult, error) {
			if len(request.Recipients) != 1 || request.Recipients[0].Alias != "biuro" {
				t.Fatalf("grant recipients=%+v", request.Recipients)
			}
			return platform.RealmGrantDialogResult{Action: platform.RealmGrantDialogWrite, RealmID: "realm-2"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRealmGrantRecipients, contract.CapRepoGrantAccess, contract.CapRepoRevokeAccess)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, RealmGrantBrowser: platformFake, Prompter: platformFake, RealmGrants: manager, Notifier: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	call := awaitCh(t, manager.calls, "realm grant")
	if call.serverID != "office" || call.repoID != "repo-1" || call.realmID != "realm-2" || call.access != contract.AccessReadWrite || call.revoke {
		t.Fatalf("grant call=%+v", call)
	}
}

func TestControllerDoesNotOfferOrExecuteGrantsForForeignRepository(t *testing.T) {
	manager := &fakeRealmGrantManager{recipients: []actions.RealmGrantRecipient{{RealmID: "realm-2", Alias: "biuro"}}, calls: make(chan realmGrantCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			if request.Servers[0].Folders[0].CanManageGrants {
				t.Fatalf("grant action offered for foreign repo: %+v", request)
			}
			return platform.SettingsDialogResult{Action: platform.SettingsDialogManageGrants, ServerID: "office", RepoID: "repo-1"}, nil
		},
	}
	view := lifecycleView(contract.CapRealmGrantRecipients, contract.CapRepoGrantAccess, contract.CapRepoRevokeAccess)
	view.Servers[0].RealmID = "guest"
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, RealmGrantBrowser: platformFake, Prompter: platformFake, RealmGrants: manager, Notifier: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case call := <-manager.calls:
		t.Fatalf("foreign grant executed: %+v", call)
	case <-time.After(150 * time.Millisecond):
	}
	if len(platformFake.Snapshot().Notifications) == 0 {
		t.Fatal("foreign grant refusal was silent")
	}
}

func TestControllerSettingsPublishesRealmDirectoryVisibility(t *testing.T) {
	manager := &fakeRealmGrantManager{calls: make(chan realmGrantCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			if len(request.Servers) != 1 || !request.Servers[0].CanSetRealmVisibility {
				t.Fatalf("realm visibility action missing: %+v", request)
			}
			return platform.SettingsDialogResult{Action: platform.SettingsDialogRealmVisibility, ServerID: "office"}, nil
		},
		RealmVisibilityFunc: func(context.Context, platform.RealmVisibilityDialogRequest) (platform.RealmVisibilityDialogResult, error) {
			return platform.RealmVisibilityDialogResult{Action: platform.RealmVisibilityDialogListed}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRealmSetVisibility)
	view.Servers[0].RealmAlias = "pracownia"
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, RealmGrantBrowser: platformFake, Prompter: platformFake, RealmGrants: manager})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	call := awaitCh(t, manager.calls, "realm visibility")
	if call.serverID != "office" || call.access != "listed" {
		t.Fatalf("visibility call=%+v", call)
	}
}
