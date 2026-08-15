package actions_test

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type recordingTimeouts struct{ minutes int }

func (r *recordingTimeouts) SetSessionTimeout(_ context.Context, _ string, minutes int) (int, error) {
	r.minutes = minutes
	return minutes, nil
}

func TestControllerSetsSessionTimeoutFromPrompt(t *testing.T) {
	timeouts := &recordingTimeouts{}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogSessionTimeout, ServerID: "office"}, nil
		},
		PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
			return platform.PromptTextResult{Value: "90"}, nil
		},
	}
	view := lifecycleView(contract.CapServerSetSessionTimeout)
	intents, cancel := setup(actions.Config{
		ViewModel:       viewCopy(view),
		SettingsBrowser: platformFake,
		Prompter:        platformFake,
		SessionTimeouts: timeouts,
		Notifier:        platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(time.Second)
	for timeouts.minutes != 90 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if timeouts.minutes != 90 {
		t.Fatalf("saved minutes = %d", timeouts.minutes)
	}
}
