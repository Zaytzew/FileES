package actions

import (
	"context"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"testing"
)

type shortcutActionFixture struct {
	reads, writes int
	visible       bool
	server, repo  string
}

func (f *shortcutActionFixture) ExplorerShortcut(_ context.Context, server, repo string, visible *bool, wcID string) (ExplorerShortcutPreference, error) {
	f.server, f.repo = server, repo
	if visible == nil {
		f.reads++
	} else {
		if wcID != "wc-one" {
			panic("confirmation lost WC binding")
		}
		f.writes++
		f.visible = *visible
	}
	return ExplorerShortcutPreference{Visible: f.visible, WCID: "wc-one"}, nil
}
func TestExplorerShortcutActionRequiresExactWCAndConfirmation(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "confirm"}[confirm], func(t *testing.T) {
			visible := false
			manager := &shortcutActionFixture{}
			vm := app.ViewModel{Connected: true, Capabilities: map[string]bool{"repo.explorer_shortcut": true}, Repos: []app.RepoViewModel{{ServerID: "one", ID: "repo", DisplayName: "Folder", Attached: true, ExplorerShortcut: &visible}, {ServerID: "two", ID: "repo", DisplayName: "Other", Attached: true}}}
			fake := &platformtest.Fake{ConfirmFunc: func(_ context.Context, r platform.ConfirmRequest) (bool, error) {
				if r.PresentationKey != "confirm.showExplorerShortcut" || r.PresentationArgs["name"] != "Folder" {
					t.Error(r)
				}
				return confirm, nil
			}}
			c := New(Config{ExplorerShortcut: manager, Prompter: fake, ViewModel: func() app.ViewModel { return vm }})
			c.startExplorerShortcut(t.Context(), "two", "repo")
			c.tasks.Wait()
			if manager.reads != 0 || manager.writes != 0 {
				t.Fatal("ordinary WC offered")
			}
			c.startExplorerShortcut(t.Context(), "one", "repo")
			c.tasks.Wait()
			if manager.reads != 1 || manager.writes != map[bool]int{false: 0, true: 1}[confirm] || manager.visible != confirm || manager.server != "one" {
				t.Fatal(manager)
			}
			vm.Stale = true
			c.startExplorerShortcut(t.Context(), "one", "repo")
			c.tasks.Wait()
			if manager.reads != 1 {
				t.Fatal("stale view changed shortcut")
			}
		})
	}
}
