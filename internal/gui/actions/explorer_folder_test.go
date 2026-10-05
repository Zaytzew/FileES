package actions_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

func TestExplorerAddUsesSelectedFolderAndOnlyEligibleServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Żółć i zdjęcia")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	fake := &platformtest.Fake{PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
		return platform.PromptTextResult{Value: "Zdjęcia"}, nil
	}, ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil }}
	prompter := &choosingPrompter{Fake: fake, choice: "office", choices: make(chan platform.ChoiceRequest, 1)}
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "ro", ClientRole: contract.ClientRoleReadOnly, CanCreateRepositories: true}, {ID: "office", DisplayName: "Pracownia", CanCreateRepositories: true}, {ID: "gone", Detached: true, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), FolderPicker: fake, Prompter: prompter, Notifier: fake, RepositoryCreator: creator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentExplorerAdd, Path: path})
	choice := awaitCh(t, prompter.choices, "server choice")
	if len(choice.Options) != 1 || choice.Options[0].Value != "office" || choice.Options[0].Label != "Pracownia" {
		t.Fatalf("options=%+v", choice.Options)
	}
	call := awaitCh(t, creator.calls, "creation")
	if call.serverID != "office" || call.localPath != path || call.displayName != "Zdjęcia" {
		t.Fatalf("call=%+v", call)
	}
	if len(fake.Snapshot().FolderRequests) != 0 {
		t.Fatal("Explorer folder picked again")
	}
}

func TestExplorerDetachAlwaysKeepsFilesEvenWithDeletingAdapter(t *testing.T) {
	path := t.TempDir()
	view := lifecycleView(contract.CapRepoDetach)
	view.Repos[0].LocalPath = path
	detacher := &localDeletingDetacher{fakeRepositoryDetacher: fakeRepositoryDetacher{calls: make(chan detachCall, 1)}, deletes: make(chan bool, 1)}
	fake := &platformtest.Fake{ConfirmFunc: func(_ context.Context, r platform.ConfirmRequest) (bool, error) {
		if r.PresentationKey != "explorer.detach" || !strings.Contains(r.Text, "pozostaną na dysku") {
			t.Errorf("confirmation=%+v", r)
		}
		return true, nil
	}}
	prompter := &choosingPrompter{Fake: fake, choice: "delete", choices: make(chan platform.ChoiceRequest, 1)}
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), Prompter: prompter, Notifier: fake, RepositoryDetacher: detacher})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentExplorerDetach, Path: path})
	call := awaitCh(t, detacher.calls, "soft detach")
	if call.deleteRepository {
		t.Fatal("server deletion")
	}
	select {
	case <-detacher.deletes:
		t.Fatal("local deletion")
	default:
	}
	select {
	case <-prompter.choices:
		t.Fatal("offered deletion choice")
	default:
	}
}

func TestExplorerRefusesSubfoldersSparsePointsStaleViewsAndUnmanagedFolders(t *testing.T) {
	for _, mode := range []string{"subfolder", "sparse", "stale", "required", "unmanaged", "cancel", "path-changed"} {
		t.Run(mode, func(t *testing.T) {
			path := t.TempDir()
			selected := path
			view := lifecycleView(contract.CapRepoDetach)
			view.Repos[0].LocalPath = path
			switch mode {
			case "subfolder":
				selected = filepath.Join(path, "child")
				if err := os.Mkdir(selected, 0700); err != nil {
					t.Fatal(err)
				}
			case "sparse":
				view.Repos[0].Sparse = true
			case "stale":
				view.Stale = true
			case "required":
				view.Repos[0].AttachmentPolicy = "required"
			case "unmanaged":
				view.Repos = nil
			}
			detacher := &fakeRepositoryDetacher{calls: make(chan detachCall, 1)}
			completed := make(chan struct{}, 1)
			fake := &platformtest.Fake{ShowInfoFunc: func(context.Context, platform.InfoRequest) error { completed <- struct{}{}; return nil }, ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) {
				if mode == "path-changed" {
					view.Repos[0].LocalPath = filepath.Join(path, "changed")
				}
				completed <- struct{}{}
				return mode != "cancel", nil
			}}
			intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, Prompter: fake, Notifier: fake, RepositoryDetacher: detacher})
			send(t, intents, tray.Intent{Kind: tray.IntentExplorerDetach, Path: selected})
			awaitCh(t, completed, "refusal or cancel")
			cancel()
			select {
			case call := <-detacher.calls:
				t.Fatalf("mutated: %+v", call)
			default:
			}
		})
	}
}

func TestExplorerAddRechecksAuthorityAfterServerChoice(t *testing.T) {
	path := t.TempDir()
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", CanCreateRepositories: true}}}
	creator := &fakeRepositoryCreator{calls: make(chan createCall, 1)}
	fake := &platformtest.Fake{}
	choice := &revokingExplorerPrompter{Fake: fake, view: &view, done: make(chan struct{}, 1)}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, Prompter: choice, FolderPicker: fake, RepositoryCreator: creator})
	send(t, intents, tray.Intent{Kind: tray.IntentExplorerAdd, Path: path})
	awaitCh(t, choice.done, "choice")
	cancel()
	select {
	case <-creator.calls:
		t.Fatal("created after revoke")
	default:
	}
}

type revokingExplorerPrompter struct {
	*platformtest.Fake
	view *app.ViewModel
	done chan struct{}
}

func (p *revokingExplorerPrompter) ChooseOne(context.Context, platform.ChoiceRequest) (string, bool, error) {
	p.view.Servers[0].CanCreateRepositories = false
	p.done <- struct{}{}
	return "office", true, nil
}

func TestExplorerAddRefusesExistingWorkingCopyBeforeServerChoice(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	fake := &platformtest.Fake{ShowInfoFunc: func(context.Context, platform.InfoRequest) error { done <- struct{}{}; return nil }}
	prompter := &choosingPrompter{Fake: fake, choices: make(chan platform.ChoiceRequest, 1)}
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(app.ViewModel{Connected: true}), Prompter: prompter})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentExplorerAdd, Path: path})
	awaitCh(t, done, "refusal")
	select {
	case <-prompter.choices:
		t.Fatal("offered duplicate import")
	case <-time.After(10 * time.Millisecond):
	}
}
