package actions_test

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
)

type invitationManager struct {
	oneChannel
	available bool
	state     string
	changes   chan actions.UploadChannelDeclaration
}

func (m invitationManager) ListUploadChannels(context.Context, string, string) (actions.UploadChannelList, error) {
	return actions.UploadChannelList{CanManageInvitations: m.available, Channels: []actions.UploadChannelSummary{{ChannelID: "channel-1", Slug: "inbox", State: m.state, Recipients: []string{"a@example.test"}, RequireOTP: true, InvitationMessage: "Zapisana wiadomość"}}}, nil
}
func (m invitationManager) UpdateUploadChannel(_ context.Context, _, _ string, body actions.UploadChannelDeclaration) error {
	m.changes <- body
	return nil
}

func TestShelfResendRequiresSupportActiveShelfAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, state                string
		available, confirmed, want bool
	}{
		{"send", "active", true, true, true}, {"cancel", "active", true, false, false},
		{"legacy", "active", false, true, false}, {"revoked", "revoked", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes, closed := make(chan actions.UploadChannelDeclaration, 1), make(chan struct{})
			manager := invitationManager{available: tc.available, state: tc.state, changes: changes}
			shown := 0
			fake := &platformtest.Fake{
				SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
					return platform.SettingsDialogResult{Action: platform.SettingsDialogUploadChannels, ServerID: "office", RepoID: "repo-1"}, nil
				},
				UploadChannelsFunc: func(_ context.Context, req platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
					if req.CanManageInvitations != tc.available {
						t.Error("server support lost")
					}
					shown++
					if shown == 1 {
						return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogResend, ChannelID: "channel-1"}, nil
					}
					close(closed)
					return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
				},
				ConfirmFunc: func(_ context.Context, req platform.ConfirmRequest) (bool, error) {
					if req.PresentationKey != "dialog.resendShelf" {
						t.Error("wrong confirmation")
					}
					return tc.confirmed, nil
				},
			}
			intents, cancel := setup(actions.Config{ViewModel: viewCopy(lifecycleView(uploadChannelCaps()...)), SettingsBrowser: fake, UploadChannelBrowser: fake, UploadChannels: manager, Prompter: fake, Notifier: fake})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("flow did not finish")
			}
			select {
			case body := <-changes:
				if !tc.want || !body.ResendInvitations || !body.ManageInvitations || body.InvitationMessage != "Zapisana wiadomość" || !body.RequireOTP || len(body.Recipients) != 1 {
					t.Fatalf("resend changed declaration: %+v", body)
				}
			default:
				if tc.want {
					t.Fatal("resend not requested")
				}
			}
		})
	}
}

func TestShelfMessageEditUsesSavedDefaultAndDoesNotResend(t *testing.T) {
	changes, closed := make(chan actions.UploadChannelDeclaration, 1), make(chan struct{})
	manager := invitationManager{available: true, state: "active", changes: changes}
	shown, messagePrompts := 0, 0
	fake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogUploadChannels, ServerID: "office", RepoID: "repo-1"}, nil
		},
		UploadChannelsFunc: func(context.Context, platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
			shown++
			if shown == 1 {
				return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogEdit, ChannelID: "channel-1"}, nil
			}
			close(closed)
			return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
		},
		PromptTextFunc: func(_ context.Context, req platform.PromptTextRequest) (platform.PromptTextResult, error) {
			if req.PresentationKey == "input.contributors" {
				return platform.PromptTextResult{Value: req.Default}, nil
			}
			if req.PresentationKey != "input.shelfMessage" || req.Default != "Zapisana wiadomość" || !req.Multiline || req.MaxLength != 2000 {
				t.Errorf("wrong message prompt: %+v", req)
			}
			messagePrompts++
			return platform.PromptTextResult{Value: "Nowa\nwiadomość"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
	}
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(lifecycleView(uploadChannelCaps()...)), SettingsBrowser: fake, UploadChannelBrowser: fake, UploadChannels: manager, Prompter: fake, Notifier: fake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("flow did not finish")
	}
	body := <-changes
	if messagePrompts != 1 || body.ResendInvitations || !body.ManageInvitations || body.InvitationMessage != "Nowa\nwiadomość" {
		t.Fatalf("edit: %+v", body)
	}
}
