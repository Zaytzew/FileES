package actions_test

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
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

type recordingActionLifecycle struct {
	started  chan app.PendingAction
	awaited  chan string
	finished chan string
}

func newRecordingActionLifecycle() *recordingActionLifecycle {
	return &recordingActionLifecycle{
		started:  make(chan app.PendingAction, 1),
		awaited:  make(chan string, 1),
		finished: make(chan string, 1),
	}
}

func (lifecycle *recordingActionLifecycle) StartAction(action app.PendingAction) bool {
	lifecycle.started <- action
	return true
}

func (lifecycle *recordingActionLifecycle) AwaitActionProjection(id string) { lifecycle.awaited <- id }
func (lifecycle *recordingActionLifecycle) FinishAction(id string)          { lifecycle.finished <- id }

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

func TestControllerFencesSessionTimeoutUntilExpectedProjection(t *testing.T) {
	timeouts := &recordingTimeouts{}
	lifecycle := newRecordingActionLifecycle()
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
		ViewModel: viewCopy(view), SettingsBrowser: platformFake, Prompter: platformFake,
		SessionTimeouts: timeouts, Notifier: platformFake, ActionLifecycle: lifecycle,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})

	var started app.PendingAction
	select {
	case started = <-lifecycle.started:
	case <-time.After(time.Second):
		t.Fatal("timeout action was not projected")
	}
	if started.ServerID != "office" || started.ExpectedSessionTimeoutMin != 90 || started.Label == "" {
		t.Fatalf("started timeout action = %+v", started)
	}
	select {
	case awaited := <-lifecycle.awaited:
		if awaited != started.ID {
			t.Fatalf("awaited id = %q, want %q", awaited, started.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout action did not enter projection fence")
	}
}
