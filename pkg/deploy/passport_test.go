package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"filees/pkg/onboarding"

	"golang.org/x/crypto/ssh"
)

func TestOnboardPassportPersistsRequestBeforeSSHAndResumes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "passport")
	// filepath.Join, not "/tmp/known_hosts": validate() demands an absolute
	// path and a leading slash is drive-relative on Windows.
	profile := ServerProfile{ID: "test", Address: "server.example.net:2222", KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
	var firstID string
	fail := func(_ context.Context, got ServerProfile, email, requestID string) (onboarding.OnboardResponse, error) {
		if got != profile {
			t.Fatalf("profile=%+v", got)
		}
		firstID = requestID
		if email != "User@example.net" {
			t.Fatalf("canonical email=%q", email)
		}
		return onboarding.OnboardResponse{}, errors.New("lost response")
	}
	if _, err := beginOnboarding(t.Context(), root, profile, "User@Example.NET", fail); err == nil {
		t.Fatal("lost response accepted")
	}
	worker := testBarePublicKey(t)
	resume := func(_ context.Context, _ ServerProfile, _ string, requestID string) (onboarding.OnboardResponse, error) {
		if requestID != firstID {
			t.Fatalf("request ID changed: %s != %s", requestID, firstID)
		}
		return onboarding.OnboardResponse{Schema: onboarding.OnboardResponseSchema, Status: "accepted", OnboardingRequestID: requestID, WorkerPublicKey: worker}, nil
	}
	passport, err := beginOnboarding(t.Context(), root, profile, "User@example.net", resume)
	if err != nil {
		t.Fatal(err)
	}
	if passport.State != passportAccepted || passport.WorkerPublicKey != worker {
		t.Fatalf("passport=%+v", passport)
	}
	called := false
	_, err = beginOnboarding(t.Context(), root, profile, "User@example.net", func(context.Context, ServerProfile, string, string) (onboarding.OnboardResponse, error) {
		called = true
		return onboarding.OnboardResponse{}, nil
	})
	if err != nil || called {
		t.Fatalf("accepted retry err=%v called=%v", err, called)
	}
}

func testBarePublicKey(t *testing.T) string {
	t.Helper()
	signer, err := BootstrapSigner()
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(signer.PublicKey()))[:len(ssh.MarshalAuthorizedKey(signer.PublicKey()))-1]
}
