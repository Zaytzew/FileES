package actions_test

import (
	"context"
	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	"testing"
	"time"
)

type shareInvitationManager struct {
	dashboardPublicShareManager
	available bool
	changes   chan actions.PublicShareDeclaration
}

func (m *shareInvitationManager) ListPublicSharesWithInvitations(context.Context, string, string) (actions.PublicShareList, error) {
	return actions.PublicShareList{CanManageInvitations: m.available, Shares: m.shares}, nil
}
func (m *shareInvitationManager) UpdatePublicShare(_ context.Context, _, _ string, d actions.PublicShareDeclaration) error {
	m.changes <- d
	return nil
}

func TestPublicShareResendRequiresCapabilityRecipientsActiveAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, state                        string
		capable, recipients, confirm, want bool
	}{
		{"send", "active", true, true, true, true}, {"cancel", "active", true, true, false, false},
		{"old server", "active", false, true, true, false}, {"revoked", "revoked", true, true, true, false}, {"open", "active", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &shareInvitationManager{available: tc.capable, changes: make(chan actions.PublicShareDeclaration, 1)}
			share := actions.PublicShareSummary{ChannelID: "share-1", State: tc.state, Slug: "project", SourceRoot: ".", InvitationMessage: "Wiadomość"}
			if tc.recipients {
				share.Recipients = []string{"a@example.test"}
			}
			m.shares = []actions.PublicShareSummary{share}
			closed := make(chan struct{})
			shown := 0
			fake := &platformtest.Fake{PublicSharesFunc: func(_ context.Context, req platform.PublicShareDialogRequest) (platform.PublicShareDialogResult, error) {
				if req.Shares[0].CanResend != (tc.capable && tc.recipients && tc.state == "active") {
					t.Error("wrong resend visibility")
				}
				shown++
				if shown == 1 {
					return platform.PublicShareDialogResult{Action: platform.PublicShareDialogResend, ChannelID: "share-1"}, nil
				}
				close(closed)
				return platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}, nil
			}, ConfirmFunc: func(_ context.Context, r platform.ConfirmRequest) (bool, error) {
				if r.PresentationKey != "dialog.resendShelf" {
					t.Error("missing confirmation")
				}
				return tc.confirm, nil
			}}
			intents, cancel := setup(actions.Config{ViewModel: viewCopy(publicShareDashboardView()), PublicShares: m, PublicShareBrowser: fake, FolderPicker: fake, Prompter: fake, Notifier: fake})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentManagePublicShares, ServerID: "office", RepoID: "repo-1", ChannelID: "share-1"})
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("flow did not finish")
			}
			select {
			case d := <-m.changes:
				if !tc.want || !d.ManageInvitations || !d.ResendInvitations || d.InvitationMessage != share.InvitationMessage || len(d.Recipients) != 1 || d.KeepPassword {
					t.Fatalf("invalid resend %+v", d)
				}
			default:
				if tc.want {
					t.Fatal("resend missing")
				}
			}
		})
	}
}
