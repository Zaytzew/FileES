package actions

import (
	"context"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"testing"
)

func TestPublicShareMessagePromptOnlyForSupportedClosedShares(t *testing.T) {
	for _, capable := range []bool{false, true} {
		for _, closed := range []bool{false, true} {
			root := t.TempDir()
			prompts := 0
			current := &PublicShareSummary{SourceRoot: ".", Slug: "test", InvitationMessage: "Saved"}
			if closed {
				current.Recipients = []string{"a@example.test"}
			}
			fake := &platformtest.Fake{PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
				return platform.PickFolderResult{Path: root}, nil
			}, PromptTextFunc: func(_ context.Context, r platform.PromptTextRequest) (platform.PromptTextResult, error) {
				if r.PresentationKey == "input.shelfMessage" {
					prompts++
					if r.Default != "Saved" || !r.Multiline || r.MaxLength != 2000 {
						t.Error("message prompt lost constraints")
					}
					return platform.PromptTextResult{Value: "New\nmessage"}, nil
				}
				return platform.PromptTextResult{Value: r.Default}, nil
			}}
			c := &Controller{cfg: Config{Prompter: fake, FolderPicker: fake}}
			d, ok := c.collectPublicShareDeclaration(t.Context(), app.RepoViewModel{ID: "repo", Attached: true, LocalPath: root}, current, capable)
			want := 0
			if capable && closed {
				want = 1
			}
			if !ok || prompts != want || d.ResendInvitations || d.ManageInvitations != capable {
				t.Fatalf("message flow %+v", d)
			}
			if want == 1 && d.InvitationMessage != "New\nmessage" {
				t.Fatal("message lost")
			}
		}
	}
}
