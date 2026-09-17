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
)

type fakeDemoActivator struct {
	*fakeActivator
	demos   chan string
	refusal actions.DemoRefusal
}

func (f *fakeDemoActivator) BeginDemo(_ context.Context, email string) (actions.ActivationTarget, actions.DemoRefusal, error) {
	f.demos <- email
	if f.refusal.Code != "" {
		return actions.ActivationTarget{}, f.refusal, nil
	}
	return actions.ActivationTarget{ServerID: "demo", Address: "demo.filees.space:22"}, actions.DemoRefusal{}, nil
}

func TestControllerDemoActivationAsksOnlyForMailboxThenOTP(t *testing.T) {
	responses := []platform.PromptTextResult{{Value: " reviewer@example.test "}, {Value: "OTP-CODE"}}
	fake := &platformtest.Fake{PromptTextFunc: func(_ context.Context, _ platform.PromptTextRequest) (platform.PromptTextResult, error) {
		result := responses[0]
		responses = responses[1:]
		return result, nil
	}}
	activator := &fakeDemoActivator{fakeActivator: &fakeActivator{begins: make(chan string, 1), finishes: make(chan string, 1)}, demos: make(chan string, 1)}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Prompter: fake, Notifier: fake, Activator: activator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivateDemo})
	if got := awaitCh(t, activator.demos, "demo begin"); got != "reviewer@example.test" {
		t.Fatalf("mailbox=%q", got)
	}
	if got := awaitCh(t, activator.finishes, "demo finish"); got != "demo|demo.filees.space:22|OTP-CODE" {
		t.Fatalf("finish=%q", got)
	}
	requests := fake.Snapshot().PromptRequests
	if len(requests) < 1 || requests[0].PresentationKey != "input.demoEmail" || requests[0].Secret {
		t.Fatalf("first prompt=%+v, want the visible mailbox form", requests)
	}
	select {
	case invitation := <-activator.begins:
		t.Fatalf("demo activation asked for an invitation: %q", invitation)
	default:
	}
}

func TestControllerDemoRefusalSaysWhenToTryAgainAndAsksNoOTP(t *testing.T) {
	fake := &platformtest.Fake{PromptTextFunc: func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
		if request.PresentationKey != "input.demoEmail" {
			t.Fatalf("refused demo asked for %q", request.PresentationKey)
		}
		return platform.PromptTextResult{Value: "reviewer@example.test"}, nil
	}}
	activator := &fakeDemoActivator{fakeActivator: &fakeActivator{finishes: make(chan string, 1)}, demos: make(chan string, 1), refusal: actions.DemoRefusal{Code: "demo_capacity", RetryAfterMinutes: 42}}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return app.ViewModel{} }, Prompter: fake, Notifier: fake, Activator: activator})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentActivateDemo})
	awaitCh(t, activator.demos, "demo begin")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if info := fake.Snapshot().InfoRequests; len(info) == 1 {
			if info[0].PresentationKey != "info.demoRefused.capacity" || info[0].PresentationArgs["minutes"] != "42" || info[0].Text != "Serwer demonstracyjny jest teraz pełny. Spróbuj ponownie za 42 min." {
				t.Fatalf("refusal=%+v", info[0])
			}
			select {
			case got := <-activator.finishes:
				t.Fatalf("refused demo reached the OTP step: %q", got)
			default:
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("demo refusal was not shown: %+v", fake.Snapshot())
}

func TestDemoButtonFollowsTheDaemonsOneShotState(t *testing.T) {
	vm := app.ViewModel{Connected: true, Capabilities: map[string]bool{"activation.begin": true}}
	for state, want := range map[string]bool{"available": true, "used": false, "": false} {
		vm.DemoActivation = state
		if got := vm.CanActivateDemo(); got != want {
			t.Fatalf("state %q: CanActivateDemo=%v want %v", state, got, want)
		}
	}
	vm.DemoActivation, vm.Stale = "available", true
	if vm.CanActivateDemo() {
		t.Fatal("stale view offered the demo")
	}
}
