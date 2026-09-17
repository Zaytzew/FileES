package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/onboarding"

	"github.com/google/uuid"
)

func TestDemoActivationPinsHostKeyAndSendsTheInstallationUID(t *testing.T) {
	base := t.TempDir()
	worker := testBarePublicKey(t)
	var sentUID, sentRequest string
	submit := func(_ context.Context, profile ServerProfile, email, uid, requestID string) (onboarding.OnboardResponse, error) {
		if profile.ID != DemoServerID || profile.Address != DemoServerAddress || email != "Reviewer@example.test" {
			t.Fatalf("profile=%+v email=%q", profile, email)
		}
		pin, err := os.ReadFile(profile.KnownHostsPath)
		if err != nil || strings.TrimSpace(string(pin)) != DemoKnownHost {
			t.Fatalf("host key not pinned before the connection: %q err=%v", pin, err)
		}
		sentUID, sentRequest = uid, requestID
		return onboarding.OnboardResponse{Schema: onboarding.OnboardResponseSchema, Status: "accepted", OnboardingRequestID: requestID, WorkerPublicKey: worker, AssignedReversePort: 42000}, nil
	}
	passport, profile, err := beginDemoWithSubmit(t.Context(), base, "Reviewer@Example.TEST", submit)
	if err != nil {
		t.Fatal(err)
	}
	state, err := LoadDemoInstallation(base)
	if err != nil || state.InstallationUID != sentUID || state.Used {
		t.Fatalf("state=%+v sent uid=%q err=%v", state, sentUID, err)
	}
	if passport.State != passportAccepted || passport.OnboardingRequestID != sentRequest || passport.RemotePort != 42000 || profile.ID != DemoServerID {
		t.Fatalf("passport=%+v profile=%+v", passport, profile)
	}
	if _, err := LoadOnboardPassport(base, profile); err != nil {
		t.Fatalf("finish cannot find the demo passport: %v", err)
	}
}

func TestDemoRefusalKeepsRetryAndFinalRefusalSpendsTheDemo(t *testing.T) {
	base := t.TempDir()
	refuse := func(code string, minutes int) demoSubmitter {
		return func(context.Context, ServerProfile, string, string, string) (onboarding.OnboardResponse, error) {
			return onboarding.OnboardResponse{}, &DemoRefusedError{Code: code, RetryAfterMinutes: minutes}
		}
	}
	_, _, err := beginDemoWithSubmit(t.Context(), base, "a@example.test", refuse(onboarding.DemoRefusedCapacity, 17))
	var refused *DemoRefusedError
	if !errors.As(err, &refused) || refused.RetryAfterMinutes != 17 {
		t.Fatalf("capacity refusal err=%v", err)
	}
	if state, _ := LoadDemoInstallation(base); state.Used {
		t.Fatal("a temporary refusal spent the demo")
	}
	// A different mailbox after a refusal starts over rather than failing.
	if _, _, err := beginDemoWithSubmit(t.Context(), base, "b@example.test", refuse(onboarding.DemoRefusedInstallation, 0)); !errors.As(err, &refused) {
		t.Fatalf("installation refusal err=%v", err)
	}
	if state, _ := LoadDemoInstallation(base); !state.Used || state.UsedReason != onboarding.DemoRefusedInstallation {
		t.Fatalf("final refusal did not spend the demo: %+v", state)
	}
	called := false
	never := func(context.Context, ServerProfile, string, string, string) (onboarding.OnboardResponse, error) {
		called = true
		return onboarding.OnboardResponse{}, nil
	}
	if _, _, err := beginDemoWithSubmit(t.Context(), base, "c@example.test", never); !errors.Is(err, ErrDemoUsed) || called {
		t.Fatalf("spent demo err=%v called=%v", err, called)
	}
}

func TestDemoUsedSurvivesRemovalOfTheServerDirectory(t *testing.T) {
	base := t.TempDir()
	if err := MarkDemoUsed(base, "activated"); err != nil {
		t.Fatal(err)
	}
	profile, err := DemoServerProfile(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(profile.KnownHostsPath)); err != nil {
		t.Fatal(err)
	}
	if state, err := LoadDemoInstallation(base); err != nil || !state.Used {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestDemoResponseIsJudgedStrictly(t *testing.T) {
	request := onboarding.OnboardRequest{Schema: onboarding.DemoOnboardRequestSchema, Email: "r@example.test", InstallationUID: uuid.NewString(), OnboardingRequestID: uuid.NewString()}
	answer := func(response onboarding.OnboardResponse) error {
		_, err := judgeDemoResponse(request, func() (onboarding.OnboardResponse, error) { return response, nil })
		return err
	}
	var refused *DemoRefusedError
	if err := answer(onboarding.OnboardResponse{Schema: onboarding.OnboardResponseSchema, Status: onboarding.DemoRefusedAddress, OnboardingRequestID: request.OnboardingRequestID, RetryAfterMinutes: 1440}); !errors.As(err, &refused) || refused.RetryAfterMinutes != 1440 {
		t.Fatalf("address refusal err=%v", err)
	}
	for name, response := range map[string]onboarding.OnboardResponse{
		"unknown status":       {Schema: onboarding.OnboardResponseSchema, Status: "maybe", OnboardingRequestID: request.OnboardingRequestID},
		"accepted with retry":  {Schema: onboarding.OnboardResponseSchema, Status: "accepted", OnboardingRequestID: request.OnboardingRequestID, RetryAfterMinutes: 5, WorkerPublicKey: testBarePublicKey(t), AssignedReversePort: 42000},
		"accepted without key": {Schema: onboarding.OnboardResponseSchema, Status: "accepted", OnboardingRequestID: request.OnboardingRequestID, AssignedReversePort: 42000},
	} {
		if err := answer(response); err == nil || errors.As(err, &refused) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
}
