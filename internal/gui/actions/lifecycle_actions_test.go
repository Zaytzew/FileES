package actions_test

import (
	"context"
	"sync"
	"testing"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type detachCall struct {
	serverID, repoID string
	deleteRepository bool
}

type fakeRepositoryDetacher struct{ calls chan detachCall }

func (fake *fakeRepositoryDetacher) DetachRepository(_ context.Context, serverID, repoID string, deleteRepository bool) error {
	fake.calls <- detachCall{serverID: serverID, repoID: repoID, deleteRepository: deleteRepository}
	return nil
}

type fakeStackLifecycle struct {
	restarts  chan struct{}
	shutdowns chan struct{}
}

func (fake *fakeStackLifecycle) RestartFileES(context.Context) error {
	fake.restarts <- struct{}{}
	return nil
}

func (fake *fakeStackLifecycle) ShutdownFileES(context.Context) error {
	fake.shutdowns <- struct{}{}
	return nil
}

func lifecycleView(capabilities ...string) app.ViewModel {
	caps := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		caps[capability] = true
	}
	repo := app.RepoViewModel{
		ID: "repo-1", ServerID: "office", DisplayName: "Dokumenty",
		Attached: true, AttachmentPolicy: "optional", OwnerRealmID: "realm-1",
		LocalPath: "/home/user/Dokumenty", Access: contract.AccessReadWrite, State: contract.StateActive,
	}
	return app.ViewModel{
		Connected: true, Capabilities: caps, Repos: []app.RepoViewModel{repo},
		Servers: []app.ServerViewModel{{
			ID: "office", RealmID: "realm-1", ClientRole: contract.ClientRoleNormal,
			CanCreateRepositories: true, Repos: []app.RepoViewModel{repo},
		}},
	}
}

func TestControllerLocalDetachUsesOneConfirmationAndDistinctCommand(t *testing.T) {
	detacher := &fakeRepositoryDetacher{calls: make(chan detachCall, 1)}
	platformFake := &platformtest.Fake{
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRepoDetach)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), Prompter: platformFake, Notifier: platformFake,
		RepositoryDetacher: detacher,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentDetachRepository, ServerID: "office", RepoID: "repo-1"})
	call := awaitCh(t, detacher.calls, "local detach")
	if call.serverID != "office" || call.repoID != "repo-1" || call.deleteRepository {
		t.Fatalf("detach call=%+v", call)
	}
	if confirmations := platformFake.Snapshot().ConfirmRequests; len(confirmations) != 1 {
		t.Fatalf("local detach confirmations=%d, want 1", len(confirmations))
	}
}

func TestControllerPermanentDeleteRequiresTwoSeparateConfirmations(t *testing.T) {
	detacher := &fakeRepositoryDetacher{calls: make(chan detachCall, 1)}
	var mu sync.Mutex
	confirmCount := 0
	platformFake := &platformtest.Fake{
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			confirmCount++
			return true, nil
		},
	}
	view := lifecycleView(contract.CapRepoDelete)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), Prompter: platformFake, Notifier: platformFake,
		RepositoryDetacher: detacher,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentDeleteRepository, ServerID: "office", RepoID: "repo-1"})
	call := awaitCh(t, detacher.calls, "permanent delete")
	if !call.deleteRepository {
		t.Fatalf("permanent delete used local detach command: %+v", call)
	}
	confirmations := platformFake.Snapshot().ConfirmRequests
	if len(confirmations) != 2 || confirmations[0].Title == confirmations[1].Title {
		t.Fatalf("confirmations=%+v", confirmations)
	}
}

func TestControllerSettingsRoutesFolderDetachThroughExistingConfirmation(t *testing.T) {
	detacher := &fakeRepositoryDetacher{calls: make(chan detachCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogDetachFolder, ServerID: "office", RepoID: "repo-1"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRepoDetach)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, RepositoryDetacher: detacher})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings})
	call := awaitCh(t, detacher.calls, "settings detach")
	if call.serverID != "office" || call.repoID != "repo-1" || call.deleteRepository {
		t.Fatalf("settings detach call=%+v", call)
	}
	if len(platformFake.Snapshot().ConfirmRequests) != 1 {
		t.Fatalf("settings detach bypassed confirmation: %#v", platformFake.Snapshot())
	}
}

func TestControllerPermanentDeleteStopsWhenSecondConfirmationIsRejected(t *testing.T) {
	detacher := &fakeRepositoryDetacher{calls: make(chan detachCall, 1)}
	count := 0
	platformFake := &platformtest.Fake{
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) {
			count++
			return count == 1, nil
		},
	}
	view := lifecycleView(contract.CapRepoDelete)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), Prompter: platformFake,
		RepositoryDetacher: detacher,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentDeleteRepository, ServerID: "office", RepoID: "repo-1"})
	assertNotReceived(t, detacher.calls, "delete after rejected second confirmation")
	if confirmations := platformFake.Snapshot().ConfirmRequests; len(confirmations) != 2 {
		t.Fatalf("confirmations=%d, want 2", len(confirmations))
	}
}

func TestControllerRestartsAndShutsDownWholeStack(t *testing.T) {
	stack := &fakeStackLifecycle{restarts: make(chan struct{}, 1), shutdowns: make(chan struct{}, 1)}
	platformFake := &platformtest.Fake{
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapSystemRestart, contract.CapSystemShutdown)
	restarted, shutdown := make(chan struct{}, 1), make(chan struct{}, 1)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), Prompter: platformFake, Stack: stack,
		Restart: func() { restarted <- struct{}{} }, Shutdown: func() { shutdown <- struct{}{} },
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentRestartFileES})
	awaitCh(t, stack.restarts, "daemon restart")
	awaitCh(t, restarted, "GUI restart")

	send(t, intents, tray.Intent{Kind: tray.IntentShutdownFileES})
	awaitCh(t, stack.shutdowns, "daemon shutdown")
	awaitCh(t, shutdown, "GUI shutdown")
}

func viewCopy(view app.ViewModel) func() app.ViewModel {
	return func() app.ViewModel { return view }
}
