package actions_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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

type repairCall struct {
	operationID, serverID, repoID, strategy string
}

type fakeRepositoryLifecycleRepairer struct{ calls chan repairCall }

func (fake *fakeRepositoryLifecycleRepairer) RepairRepositoryLifecycle(_ context.Context, operationID, serverID, repoID, strategy string) (string, error) {
	fake.calls <- repairCall{operationID: operationID, serverID: serverID, repoID: repoID, strategy: strategy}
	return "repository_created", nil
}

type loadDumpCall struct{ serverID, repoID string }

type fakeRepositoryDumpLoader struct{ calls chan loadDumpCall }

func (fake *fakeRepositoryDumpLoader) LoadDump(_ context.Context, serverID, repoID string) error {
	fake.calls <- loadDumpCall{serverID: serverID, repoID: repoID}
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

func TestControllerFencesDetachUntilRepositoryProjectionChanges(t *testing.T) {
	detacher := &fakeRepositoryDetacher{calls: make(chan detachCall, 1)}
	lifecycle := newRecordingActionLifecycle()
	platformFake := &platformtest.Fake{
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRepoDetach)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), Prompter: platformFake, Notifier: platformFake,
		RepositoryDetacher: detacher, ActionLifecycle: lifecycle,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentDetachRepository, ServerID: "office", RepoID: "repo-1"})
	started := awaitCh(t, lifecycle.started, "detach action")
	if started.ServerID != "office" || started.RepoID != "repo-1" || !started.ExpectedRepoDetached || started.ExpectedRepoDeleted {
		t.Fatalf("detach pending action = %+v", started)
	}
	if awaited := awaitCh(t, lifecycle.awaited, "detach projection fence"); awaited != started.ID {
		t.Fatalf("awaited id=%q, want %q", awaited, started.ID)
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

func TestControllerDeletesOwnedRemoteRepositoryWithoutLocalAttachment(t *testing.T) {
	detacher := &fakeRepositoryDetacher{calls: make(chan detachCall, 1)}
	platformFake := &platformtest.Fake{
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRepoDelete)
	view.Repos[0].Attached = false
	view.Repos[0].LocalPath = ""
	view.Repos[0].State = contract.StateUnattached
	view.Servers[0].Repos[0] = view.Repos[0]
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), Prompter: platformFake, Notifier: platformFake,
		RepositoryDetacher: detacher,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentDeleteRepository, ServerID: "office", RepoID: "repo-1"})
	call := awaitCh(t, detacher.calls, "remote repository delete")
	if !call.deleteRepository || call.serverID != "office" || call.repoID != "repo-1" {
		t.Fatalf("remote delete call=%+v", call)
	}
	confirmations := platformFake.Snapshot().ConfirmRequests
	if len(confirmations) != 2 || !strings.Contains(confirmations[0].Text, "Nie ma przypiętego lokalnego folderu") || strings.Contains(confirmations[0].Text, "/home/user") {
		t.Fatalf("remote delete confirmations=%+v", confirmations)
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
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	call := awaitCh(t, detacher.calls, "settings detach")
	if call.serverID != "office" || call.repoID != "repo-1" || call.deleteRepository {
		t.Fatalf("settings detach call=%+v", call)
	}
	if len(platformFake.Snapshot().ConfirmRequests) != 1 {
		t.Fatalf("settings detach bypassed confirmation: %#v", platformFake.Snapshot())
	}
}

func TestControllerSettingsRetriesExactProjectedLifecycleOperation(t *testing.T) {
	repairer := &fakeRepositoryLifecycleRepairer{calls: make(chan repairCall, 1)}
	lifecycle := newRecordingActionLifecycle()
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogRetryLifecycle, ServerID: "office", RepoID: "repo-1"}, nil
		},
	}
	view := lifecycleView(contract.CapRepoLifecycleRepair)
	view.Repos[0].LifecycleOperationID = "op-current"
	view.Repos[0].LifecycleError = "initial import failed"
	view.Repos[0].CanRetryLifecycle = true
	view.Servers[0].Repos[0] = view.Repos[0]
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, RepositoryRepairer: repairer, ActionLifecycle: lifecycle})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	call := awaitCh(t, repairer.calls, "lifecycle retry")
	if call.operationID != "op-current" || call.serverID != "office" || call.repoID != "repo-1" || call.strategy != "retry" {
		t.Fatalf("repair call=%+v", call)
	}
	started := awaitCh(t, lifecycle.started, "repair action")
	if started.ExpectedLifecycleOperationID != "op-current" || started.ServerID != "office" || started.RepoID != "repo-1" {
		t.Fatalf("repair pending action=%+v", started)
	}
	if awaited := awaitCh(t, lifecycle.awaited, "repair projection fence"); awaited != started.ID {
		t.Fatalf("awaited=%q want=%q", awaited, started.ID)
	}
	if len(platformFake.Snapshot().ConfirmRequests) != 0 {
		t.Fatal("safe retry unexpectedly requested destructive confirmation")
	}
}

func TestControllerSettingsConfirmsLocalLifecycleAbandon(t *testing.T) {
	repairer := &fakeRepositoryLifecycleRepairer{calls: make(chan repairCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogAbandonLifecycle, ServerID: "office", RepoID: "repo-1"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRepoLifecycleRepair)
	view.Repos[0].LifecycleOperationID = "op-current"
	view.Repos[0].LifecycleError = "checkout failed"
	view.Repos[0].CanAbandonLifecycle = true
	view.Servers[0].Repos[0] = view.Repos[0]
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, RepositoryRepairer: repairer})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	call := awaitCh(t, repairer.calls, "lifecycle abandon")
	if call.operationID != "op-current" || call.strategy != "abandon" {
		t.Fatalf("repair call=%+v", call)
	}
	confirmations := platformFake.Snapshot().ConfirmRequests
	if len(confirmations) != 1 || !strings.Contains(confirmations[0].Text, "zachowa wszystkie pliki") {
		t.Fatalf("abandon confirmation=%+v", confirmations)
	}
}

func TestControllerSettingsRoutesLoadDumpThroughConfirmation(t *testing.T) {
	loader := &fakeRepositoryDumpLoader{calls: make(chan loadDumpCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogLoadDump, ServerID: "office", RepoID: "repo-1"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRepoCreateRequest)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, RepositoryDumpLoader: loader})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	call := awaitCh(t, loader.calls, "settings load dump")
	if call.serverID != "office" || call.repoID != "repo-1" {
		t.Fatalf("load dump call=%+v", call)
	}
	if len(platformFake.Snapshot().ConfirmRequests) != 1 {
		t.Fatalf("load dump bypassed confirmation: %#v", platformFake.Snapshot())
	}
}

func TestControllerLoadDumpStopsWhenConfirmationIsRejected(t *testing.T) {
	loader := &fakeRepositoryDumpLoader{calls: make(chan loadDumpCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogLoadDump, ServerID: "office", RepoID: "repo-1"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return false, nil },
	}
	view := lifecycleView(contract.CapRepoCreateRequest)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, RepositoryDumpLoader: loader})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case call := <-loader.calls:
		t.Fatalf("load dump proceeded after a rejected confirmation: %+v", call)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestControllerLoadDumpRefusesRepositoryNotOwnedByCurrentRealm(t *testing.T) {
	loader := &fakeRepositoryDumpLoader{calls: make(chan loadDumpCall, 1)}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogLoadDump, ServerID: "office", RepoID: "repo-1"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	view := lifecycleView(contract.CapRepoCreateRequest)
	// A guest rw-grant on a foreign repo must never reach LoadDump: the
	// server-side gate is ownership + can-create-repositories, and the GUI
	// mirrors it rather than relying on the server to reject after the fact.
	view.Repos[0].OwnerRealmID = "realm-2"
	view.Servers[0].Repos[0].OwnerRealmID = "realm-2"
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake, RepositoryDumpLoader: loader})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case call := <-loader.calls:
		t.Fatalf("load dump proceeded on a repository not owned by the current realm: %+v", call)
	case <-time.After(50 * time.Millisecond):
	}
	if len(platformFake.Snapshot().ConfirmRequests) != 0 {
		t.Fatal("confirmation shown for a repository the realm does not own")
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
