package deploy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestActivationRunnerPersistsBindingsBeforeOTPAndReusesThem(t *testing.T) {
	root := filepath.Join(t.TempDir(), "activation")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	profile := ServerProfile{ID: "test", Address: "server.example.net:2222", KnownHostsPath: knownHosts}
	passport := OnboardPassport{
		Schema: OnboardPassportSchema, State: passportAccepted, Email: "user@example.net",
		OnboardingRequestID: uuid.NewString(), WorkerPublicKey: testBarePublicKey(t),
		ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: profile.KnownHostsPath,
	}
	prover := SSHAccessProver{Address: "127.0.0.1:1", IdentityRoot: filepath.Join(root, profile.ID, "identity")}
	var first TunnelSpec
	start := func(_ context.Context, spec TunnelSpec, otp []byte) error {
		if string(otp) != "one-time" {
			t.Fatalf("OTP=%q", otp)
		}
		first = spec
		return nil
	}
	opts := ActivationOptions{Root: root, ServerProfile: profile, RemotePort: 42000}
	if err := runActivation(t.Context(), passport, opts, []byte("one-time"), prover, start); err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first.DeployRequestID); err != nil {
		t.Fatalf("deploy request ID=%q", first.DeployRequestID)
	}
	if first.ReconnectPublicKey == "" || first.HelperEndpoint.HostPublicKey == "" {
		t.Fatalf("incomplete tunnel spec: %+v", first)
	}
	if err := runActivation(t.Context(), passport, opts, []byte("one-time"), prover, func(_ context.Context, spec TunnelSpec, _ []byte) error {
		if spec.DeployRequestID != first.DeployRequestID || spec.ReconnectPublicKey != first.ReconnectPublicKey || spec.HelperEndpoint.HostPublicKey != first.HelperEndpoint.HostPublicKey {
			t.Fatalf("activation bindings changed across retry")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
