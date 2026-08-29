package actions_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type recordingUploadChannels struct {
	mu      sync.Mutex
	creates []actions.UploadChannelDeclaration
}

func (r *recordingUploadChannels) ListUploadChannels(context.Context, string, string) ([]actions.UploadChannelSummary, error) {
	return nil, nil
}
func (r *recordingUploadChannels) CreateUploadChannel(_ context.Context, _ string, declaration actions.UploadChannelDeclaration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates = append(r.creates, declaration)
	return nil
}
func (recordingUploadChannels) UpdateUploadChannel(context.Context, string, string, actions.UploadChannelDeclaration) error {
	return nil
}
func (recordingUploadChannels) RevokeUploadChannel(context.Context, string, string, string) error {
	return nil
}
func (recordingUploadChannels) DeleteUploadChannel(context.Context, string, string, string) error {
	return nil
}

func (r *recordingUploadChannels) created() []actions.UploadChannelDeclaration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]actions.UploadChannelDeclaration, len(r.creates))
	copy(out, r.creates)
	return out
}

func uploadChannelCaps() []string {
	return []string{contract.CapRepoUploadChannelList, contract.CapRepoUploadChannelCreate, contract.CapRepoUploadChannelUpdate, contract.CapRepoUploadChannelRevoke, contract.CapRepoUploadChannelDelete}
}

func TestControllerCreateUploadShelfCollectsSlugAndRecipientsWithoutOTP(t *testing.T) {
	manager := &recordingUploadChannels{}
	dialogs := 0
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogUploadChannels, ServerID: "office", RepoID: "repo-1"}, nil
		},
		UploadChannelsFunc: func(context.Context, platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
			dialogs++
			if dialogs == 1 {
				return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogCreate}, nil
			}
			return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
		},
		PromptTextFunc: func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
			switch request.Title {
			case "Adres półki":
				return platform.PromptTextResult{Value: "Oferta-A"}, nil
			case "Wnoszący":
				return platform.PromptTextResult{Value: "a@example.com; B@example.com"}, nil
			default:
				t.Fatalf("unexpected prompt %q", request.Title)
				return platform.PromptTextResult{}, nil
			}
		},
	}
	view := lifecycleView(uploadChannelCaps()...)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), SettingsBrowser: platformFake, UploadChannelBrowser: platformFake,
		Prompter: platformFake, UploadChannels: manager, Notifier: platformFake,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(2 * time.Second)
	for len(manager.created()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	created := manager.created()
	if len(created) != 1 || created[0].AuthorityRepoID != "repo-1" || created[0].Slug != "oferta-a" || strings.Join(created[0].Recipients, ",") != "a@example.com,B@example.com" {
		t.Fatalf("created=%+v", created)
	}
	for _, prompt := range platformFake.Snapshot().PromptRequests {
		if strings.Contains(strings.ToLower(prompt.Title), "otp") || strings.Contains(strings.ToLower(prompt.Text), "otp") {
			t.Fatalf("shelf create prompted for OTP: %+v", prompt)
		}
	}
	if created[0].RequireOTP {
		t.Fatal("default Confirm must leave RequireOTP off")
	}
}

func TestControllerCreateUploadShelfCollectsOTPWhenConfirmed(t *testing.T) {
	manager := &recordingUploadChannels{}
	dialogs := 0
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogUploadChannels, ServerID: "office", RepoID: "repo-1"}, nil
		},
		UploadChannelsFunc: func(context.Context, platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
			dialogs++
			if dialogs == 1 {
				return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogCreate}, nil
			}
			return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
		},
		PromptTextFunc: func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
			switch request.Title {
			case "Adres półki":
				return platform.PromptTextResult{Value: "oferta-a"}, nil
			case "Wnoszący":
				return platform.PromptTextResult{Value: "a@example.com"}, nil
			default:
				t.Fatalf("unexpected prompt %q", request.Title)
				return platform.PromptTextResult{}, nil
			}
		},
		ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
			if request.Title != "Kod z poczty" {
				t.Fatalf("unexpected confirm %q", request.Title)
			}
			return true, nil
		},
	}
	view := lifecycleView(uploadChannelCaps()...)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), SettingsBrowser: platformFake, UploadChannelBrowser: platformFake,
		Prompter: platformFake, UploadChannels: manager, Notifier: platformFake,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(2 * time.Second)
	for len(manager.created()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	created := manager.created()
	if len(created) != 1 || !created[0].RequireOTP || created[0].Slug != "oferta-a" {
		t.Fatalf("created=%+v", created)
	}
}

func TestControllerCreateUploadShelfRejectsEmptyRecipients(t *testing.T) {
	manager := &recordingUploadChannels{}
	dialogs := 0
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogUploadChannels, ServerID: "office", RepoID: "repo-1"}, nil
		},
		UploadChannelsFunc: func(context.Context, platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
			dialogs++
			if dialogs == 1 {
				return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogCreate}, nil
			}
			return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
		},
		PromptTextFunc: func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
			if request.Title == "Adres półki" {
				return platform.PromptTextResult{Value: "oferta-a"}, nil
			}
			return platform.PromptTextResult{Value: "   "}, nil
		},
	}
	view := lifecycleView(uploadChannelCaps()...)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), SettingsBrowser: platformFake, UploadChannelBrowser: platformFake,
		Prompter: platformFake, UploadChannels: manager, Notifier: platformFake,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	waitForInfoRequest(t, platformFake)
	if created := manager.created(); len(created) != 0 {
		t.Fatalf("empty recipients created shelf: %+v", created)
	}
	snapshot := platformFake.Snapshot()
	if len(snapshot.InfoRequests) == 0 || !strings.Contains(snapshot.InfoRequests[0].Text, "Anonimowe") {
		t.Fatalf("info = %+v", snapshot.InfoRequests)
	}
}

type failingUploadChannels struct{ err error }

func (f failingUploadChannels) ListUploadChannels(context.Context, string, string) ([]actions.UploadChannelSummary, error) {
	return nil, f.err
}
func (failingUploadChannels) CreateUploadChannel(context.Context, string, actions.UploadChannelDeclaration) error {
	return nil
}
func (failingUploadChannels) UpdateUploadChannel(context.Context, string, string, actions.UploadChannelDeclaration) error {
	return nil
}
func (failingUploadChannels) RevokeUploadChannel(context.Context, string, string, string) error {
	return nil
}
func (failingUploadChannels) DeleteUploadChannel(context.Context, string, string, string) error {
	return nil
}

func TestControllerUploadChannelListFailureUsesForegroundModal(t *testing.T) {
	platformFake := &platformtest.Fake{SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogUploadChannels, ServerID: "office", RepoID: "repo-1"}, nil
	}}
	view := lifecycleView(uploadChannelCaps()...)
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(view), SettingsBrowser: platformFake, UploadChannelBrowser: platformFake,
		Prompter: platformFake, UploadChannels: failingUploadChannels{err: errors.New("upload backend failed")}, Notifier: platformFake,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	waitForInfoRequest(t, platformFake)
	snapshot := platformFake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || !strings.Contains(snapshot.InfoRequests[0].Text, "backend failed") {
		t.Fatalf("foreground error = %+v", snapshot.InfoRequests)
	}
}
