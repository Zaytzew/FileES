package actions_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type archiveCreator struct {
	fakeRepositoryCreator
	archives chan string
}

func (a *archiveCreator) CreateRepositoryFromArchive(_ context.Context, serverID, name, target, source string) (string, error) {
	a.archives <- source
	a.calls <- createCall{serverID: serverID, displayName: name, localPath: target}
	return "operation", nil
}

type archiveChoice struct {
	*platformtest.Fake
	choose func() (string, bool)
}

func (a archiveChoice) ChooseOne(context.Context, platform.ChoiceRequest) (string, bool, error) {
	value, ok := a.choose()
	return value, ok, nil
}

func TestArchiveCreationPickerCancelAndCapability(t *testing.T) {
	for _, mode := range []string{"archive", "empty", "choice-cancel", "file-cancel", "confirm-cancel", "old-daemon", "nonempty", "stale"} {
		t.Run(mode, func(t *testing.T) {
			target, source := t.TempDir(), filepath.Join(t.TempDir(), "backup.dmp")
			if mode == "nonempty" {
				_ = os.WriteFile(filepath.Join(target, "work"), []byte("keep"), 0600)
			}
			creator := &archiveCreator{fakeRepositoryCreator: fakeRepositoryCreator{calls: make(chan createCall, 1)}, archives: make(chan string, 1)}
			view := app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoCreateArchive: mode != "old-daemon"}, Servers: []app.ServerViewModel{{ID: "office", CanCreateRepositories: true}}}
			fake := &platformtest.Fake{
				PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
					return platform.PickFolderResult{Path: target}, nil
				},
				PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
					return platform.PromptTextResult{Value: "Project"}, nil
				},
				PickFilesFunc: func(_ context.Context, r platform.PickFilesRequest) (platform.PickFilesResult, error) {
					if !r.AllowOutsideRoot || r.AllowMultiple {
						t.Error("external single-file picker not used")
					}
					return platform.PickFilesResult{Paths: []string{source}, Cancelled: mode == "file-cancel"}, nil
				},
				ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) {
					if mode == "stale" {
						view.Stale = true
					}
					return mode != "confirm-cancel", nil
				},
			}
			prompter := archiveChoice{fake, func() (string, bool) {
				if mode == "empty" {
					return "empty", true
				}
				return "archive", mode != "choice-cancel"
			}}
			intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Picker: fake, Prompter: prompter, RepositoryCreator: creator, Notifier: fake})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
			shouldCreate := mode == "archive" || mode == "empty" || mode == "old-daemon" || mode == "nonempty"
			if shouldCreate {
				call := awaitCh(t, creator.calls, "create")
				if call.localPath != target {
					t.Fatal(call)
				}
				if mode == "archive" {
					if got := awaitCh(t, creator.archives, "archive"); got != source {
						t.Fatal(got)
					}
				} else {
					assertNotReceived(t, creator.archives, "unexpected archive")
				}
			} else {
				select {
				case <-creator.calls:
					t.Fatal("canceled or stale create mutated daemon")
				case <-time.After(100 * time.Millisecond):
				}
			}
		})
	}
}
