package actions_test

import (
	"context"
	"strings"
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
	listedRepo string
}

func (fake *fakeRealmGrantManager) SetEditingPolicy(_ context.Context, serverID, repoID string, lockRequired bool) (bool, error) {
	fake.calls <- realmGrantCall{serverID: serverID, repoID: repoID}
	return lockRequired, nil
}

func (fake *fakeRealmGrantManager) ListRecipients(_ context.Context, _, repoID string) ([]actions.RealmGrantRecipient, error) {
	fake.listedRepo = repoID
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
	manager := &fakeRealmGrantManager{recipients: []actions.RealmGrantRecipient{{RealmID: "realm-2", Alias: "biuro", Access: "r", State: "active"}}, calls: make(chan realmGrantCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			if len(request.Servers) != 1 || len(request.Servers[0].Folders) != 1 || !request.Servers[0].Folders[0].CanManageGrants {
				t.Fatalf("grant action not offered for owned repo: %+v", request)
			}
			return platform.SettingsDialogResult{Action: platform.SettingsDialogManageGrants, ServerID: "office", RepoID: "repo-1"}, nil
		},
		RealmGrantsFunc: func(_ context.Context, request platform.RealmGrantDialogRequest) (platform.RealmGrantDialogResult, error) {
			if len(request.Recipients) != 1 || request.Recipients[0].Alias != "biuro" || request.Recipients[0].Access != "r" || request.Recipients[0].State != "active" {
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
	if manager.listedRepo != "repo-1" {
		t.Fatalf("grant list repo=%q", manager.listedRepo)
	}
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

func TestControllerSettingsHidesRealmVisibilityUntilAliasProjectionArrives(t *testing.T) {
	platformFake := &platformtest.Fake{SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
		if len(request.Servers) != 1 || request.Servers[0].CanSetRealmVisibility {
			t.Fatalf("visibility offered without projected realm alias: %+v", request)
		}
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
	}}
	view := lifecycleView(contract.CapRealmSetVisibility)
	view.Servers[0].RealmAlias = ""
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, RealmGrantBrowser: platformFake, Prompter: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().SettingsRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(platformFake.Snapshot().SettingsRequests) != 1 || len(platformFake.Snapshot().RealmVisibilityRequests) != 0 {
		t.Fatalf("settings/visibility requests=%+v/%+v", platformFake.Snapshot().SettingsRequests, platformFake.Snapshot().RealmVisibilityRequests)
	}
}

// The owner gets the action; changing the working rules of a repository is
// not something a guest may do, and the dialog must not offer a button whose
// controller would refuse it.
func TestEditingPolicyActionOfferedToOwnerAndAppliedAfterConfirmation(t *testing.T) {
	manager := &fakeRealmGrantManager{calls: make(chan realmGrantCall, 1)}
	var confirmed platform.ConfirmRequest
	platformFake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			folder := request.Servers[0].Folders[0]
			if !folder.CanSetEditingPolicy {
				t.Fatalf("owner was not offered the editing policy action: %+v", folder)
			}
			// Every client sees the current rule spelled out, so a read-only
			// file is never unexplained.
			if folder.Editing != "swobodna" {
				t.Fatalf("editing policy not rendered for the row: %+v", folder)
			}
			return platform.SettingsDialogResult{Action: platform.SettingsDialogEditingPolicy, ServerID: "office", RepoID: "repo-1"}, nil
		},
		ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
			confirmed = request
			return true, nil
		},
	}
	view := lifecycleView(contract.CapRepoSetEditingPolicy)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, RealmGrants: manager, Notifier: platformFake})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	call := awaitCh(t, manager.calls, "editing policy")
	if call.serverID != "office" || call.repoID != "repo-1" {
		t.Fatalf("call=%+v", call)
	}
	// The consequence that matters is that the change is published to every
	// other machine, so the confirmation has to say so rather than presenting
	// this as a local preference.
	if !strings.Contains(confirmed.Text, "wszystkie komputery") {
		t.Fatalf("confirmation does not state the shared consequence: %q", confirmed.Text)
	}
}

// A guest holding rw must not see the action at all: the server would refuse
// it, and offering a button that always fails is the "click it, nothing
// happens" defect this dialog's Can* flags exist to prevent.
func TestEditingPolicyActionHiddenFromNonOwner(t *testing.T) {
	offered := make(chan bool, 1)
	platformFake := &platformtest.Fake{
		SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			offered <- request.Servers[0].Folders[0].CanSetEditingPolicy
			return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
		},
	}
	view := lifecycleView(contract.CapRepoSetEditingPolicy)
	// Same repository, but this client's realm is not its owner.
	view.Servers[0].RealmID = "realm-other"
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, Notifier: platformFake})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	if <-offered {
		t.Fatal("a realm that does not own the repository was offered the editing policy action")
	}
}
