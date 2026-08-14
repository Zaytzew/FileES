package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestInvitationPassportSameTokenResumesWithoutSecondSubmit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "passport")
	profile := ServerProfile{ID: "spot", Address: "spot.example.net:2223", KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
	token := strings.Repeat("A", 43)
	worker := testBarePublicKey(t)
	calls := 0
	submit := func(_ context.Context, _ ServerProfile, gotToken, _ string, requestID string) (onboarding.OnboardResponse, error) {
		calls++
		if gotToken != token {
			t.Fatalf("token=%q", gotToken)
		}
		return onboarding.OnboardResponse{Schema: onboarding.OnboardResponseSchema, Status: "accepted", OnboardingRequestID: requestID, WorkerPublicKey: worker, AssignedReversePort: 42000}, nil
	}
	first, err := beginInvitationWithSubmit(t.Context(), root, profile, token, submit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := beginInvitationWithSubmit(t.Context(), root, profile, token, submit)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || second.OnboardingRequestID != first.OnboardingRequestID || second.InvitationTokenHash != invitationTokenHash(token) {
		t.Fatalf("calls=%d first=%+v second=%+v", calls, first, second)
	}
}

func TestNewInvitationSupersedesAcceptedAttemptAndEphemeralIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "passport")
	profile := ServerProfile{ID: "spot", Address: "spot.example.net:2223", KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
	oldToken := strings.Repeat("A", 43)
	newToken := strings.Repeat("B", 43)
	worker := testBarePublicKey(t)
	submit := func(_ context.Context, _ ServerProfile, _ string, _ string, requestID string) (onboarding.OnboardResponse, error) {
		return onboarding.OnboardResponse{Schema: onboarding.OnboardResponseSchema, Status: "accepted", OnboardingRequestID: requestID, WorkerPublicKey: worker, AssignedReversePort: 42000}, nil
	}
	old, err := beginInvitationWithSubmit(t.Context(), root, profile, oldToken, submit)
	if err != nil {
		t.Fatal(err)
	}
	session, err := prepareActivationSession(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "identity"), filepath.Join(root, session.DeployRequestID)} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "stale"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "helper_host_ed25519"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := beginInvitationWithSubmit(t.Context(), root, profile, newToken, submit)
	if err != nil {
		t.Fatal(err)
	}
	if next.OnboardingRequestID == old.OnboardingRequestID || next.ProposedRealmID == old.ProposedRealmID || next.InvitationTokenHash != invitationTokenHash(newToken) {
		t.Fatalf("old=%+v next=%+v", old, next)
	}
	for _, path := range []string{filepath.Join(root, "identity"), filepath.Join(root, "activation.json"), filepath.Join(root, "helper_host_ed25519"), filepath.Join(root, session.DeployRequestID)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("superseded artifact remains at %s: %v", path, err)
		}
	}
}

func TestNewInvitationCannotOverwriteActiveClientProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "passport")
	profile := ServerProfile{ID: "spot", Address: "spot.example.net:2223", KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
	worker := testBarePublicKey(t)
	submit := func(_ context.Context, _ ServerProfile, _ string, _ string, requestID string) (onboarding.OnboardResponse, error) {
		return onboarding.OnboardResponse{Schema: onboarding.OnboardResponseSchema, Status: "accepted", OnboardingRequestID: requestID, WorkerPublicKey: worker, AssignedReversePort: 42000}, nil
	}
	if _, err := beginInvitationWithSubmit(t.Context(), root, profile, strings.Repeat("A", 43), submit); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "client-profile.json"), []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := beginInvitationWithSubmit(t.Context(), root, profile, strings.Repeat("B", 43), submit); err == nil || !strings.Contains(err.Error(), "already activated") {
		t.Fatalf("replacement error=%v", err)
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
