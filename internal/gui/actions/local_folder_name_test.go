package actions

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	contract "filees/pkg/contract/v1"
)

type localRenameRelocator struct {
	target string
	err    error
}

func (r *localRenameRelocator) RelocateRepository(_ context.Context, _, _, target string) (string, error) {
	r.target = target
	return "rename", r.err
}
func (r *localRenameRelocator) RelocationStatus(context.Context, string) (string, string, error) {
	return "attached", "", nil
}

func TestLocalFolderNameUsesExistingRelocation(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		for _, variant := range []string{"rename", "move", "same", "cancel", "separator", "traversal", "reserved", "trailing-space", "collision", "unconfirmed"} {
			t.Run(variant+map[bool]string{false: "-full", true: "-sparse"}[sparse], func(t *testing.T) {
				parent := t.TempDir()
				source := filepath.Join(parent, "stara nazwa")
				chosenParent := parent
				chosenName := "Łódź 東京"
				if variant == "move" {
					chosenParent = filepath.Join(parent, "inny")
					chosenName = "stara nazwa"
				}
				if variant == "same" {
					chosenName = "stara nazwa"
				}
				if variant == "separator" {
					chosenName = `inny\folder`
				}
				if variant == "traversal" {
					chosenName = ".."
				}
				if variant == "reserved" {
					chosenName = "CON.txt"
				}
				if variant == "trailing-space" {
					chosenName = "nazwa "
				}
				vm := app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoRelocate: true}, Repos: []app.RepoViewModel{{ID: "repo", ServerID: "s", Purpose: "", Attached: true, State: contract.StateActive, LocalPath: source, Sparse: sparse}}}
				r := &localRenameRelocator{}
				if variant == "collision" {
					r.err = errors.New("destination folder must not exist")
				}
				prompted, confirmed := false, false
				fake := &platformtest.Fake{
					PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
						return platform.PickFolderResult{Path: chosenParent}, nil
					},
					PromptTextFunc: func(_ context.Context, req platform.PromptTextRequest) (platform.PromptTextResult, error) {
						prompted = true
						if req.Default != "stara nazwa" || req.PresentationKey != "input.localFolderName" {
							t.Error(req)
						}
						return platform.PromptTextResult{Value: chosenName, Cancelled: variant == "cancel"}, nil
					},
					ConfirmFunc: func(_ context.Context, req platform.ConfirmRequest) (bool, error) {
						confirmed = true
						if req.PresentationArgs["source"] != source || req.PresentationArgs["target"] != filepath.Join(chosenParent, chosenName) {
							t.Error(req)
						}
						return variant != "unconfirmed", nil
					},
				}
				c := New(Config{ViewModel: func() app.ViewModel { return vm }, FolderPicker: fake, Prompter: fake, Notifier: fake, RepositoryRelocator: r})
				c.startMoveRepository(context.Background(), "s", "repo")
				c.tasks.Wait()
				if !prompted {
					t.Fatal("name field missing")
				}
				allowed := variant == "rename" || variant == "move" || variant == "collision"
				if allowed && r.target != filepath.Join(chosenParent, chosenName) {
					t.Fatal(r.target)
				}
				if !allowed && r.target != "" {
					t.Fatal("invalid rename submitted", r.target)
				}
				if !allowed && variant != "unconfirmed" && confirmed {
					t.Fatal("invalid name reached confirmation")
				}
			})
		}
	}
}
