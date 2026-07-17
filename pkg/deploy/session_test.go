package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTunnelSessionIsStrictAndPinsOneEd25519Key(t *testing.T) {
	key, err := BootstrapAuthorizedKey()
	if err != nil {
		t.Fatal(err)
	}
	wanted := TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: uuid.NewString(), HelperHostPublicKey: key, ReconnectPublicKey: key}
	raw, err := EncodeTunnelSession(wanted)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTunnelSession(strings.NewReader(string(raw)))
	if err != nil || got != wanted {
		t.Fatalf("session=%+v err=%v", got, err)
	}
	for _, raw := range []string{
		`{}`,
		`{"schema":"filees.tunnel-session/v2","deploy_request_id":"bad","helper_host_public_key":"` + key + `","reconnect_public_key":"` + key + `"}`,
		string(raw) + `{}`,
		`{"schema":"filees.tunnel-session/v2","deploy_request_id":"` + uuid.NewString() + `","helper_host_public_key":"ssh-rsa AAAA","reconnect_public_key":"` + key + `"}`,
	} {
		if _, err := DecodeTunnelSession(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted invalid tunnel frame: %q", raw)
		}
	}
}

func TestGenerateIdentityThroughHelperBoundsHostileResponse(t *testing.T) {
	worker := newTestSigner(t)
	operationID, clientID := uuid.NewString(), "client-a"
	helper, err := StartHelper(context.Background(), HelperConfig{
		OperationID: operationID,
		ClientID:    clientID,
		WorkerKey:   worker.PublicKey(),
		Identity: fixedIdentityGenerator{identity: Identity{
			Schema: IdentitySchema, OperationID: operationID, ClientID: clientID,
			State: identityActive, PublicKey: strings.Repeat("x", 4*1024*1024), Fingerprint: "oversized",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer helper.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = GenerateIdentityThroughHelper(ctx, helper.Endpoint().Address, helper.Endpoint().HostPublicKey, worker, helperRequest(operationID, clientID))
	if err == nil || err.Error() != "helper response exceeds 64 KiB" {
		t.Fatalf("oversized helper response error = %v", err)
	}
}

func TestGenerateIdentityThroughHelperDeadlineCoversActiveSSHSession(t *testing.T) {
	worker := newTestSigner(t)
	operationID, clientID := uuid.NewString(), "client-a"
	blocking := blockingIdentityGenerator{started: make(chan struct{}), release: make(chan struct{})}
	helper, err := StartHelper(context.Background(), HelperConfig{
		OperationID: operationID, ClientID: clientID, WorkerKey: worker.PublicKey(), Identity: blocking,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go func() {
		_, err := GenerateIdentityThroughHelper(ctx, helper.Endpoint().Address, helper.Endpoint().HostPublicKey, worker, helperRequest(operationID, clientID))
		result <- err
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		close(blocking.release)
		_ = helper.Close()
		t.Fatal("helper action did not start")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("active SSH session ignored its context deadline")
		}
	case <-time.After(time.Second):
		close(blocking.release)
		_ = helper.Close()
		t.Fatal("active SSH session remained blocked past its deadline")
	}
	close(blocking.release)
	if err := helper.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateIdentityThroughHelperCancellationClosesConnection(t *testing.T) {
	worker := newTestSigner(t)
	operationID, clientID := uuid.NewString(), "client-a"
	blocking := blockingIdentityGenerator{started: make(chan struct{}), release: make(chan struct{})}
	helper, err := StartHelper(context.Background(), HelperConfig{
		OperationID: operationID, ClientID: clientID, WorkerKey: worker.PublicKey(), Identity: blocking,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := GenerateIdentityThroughHelper(ctx, helper.Endpoint().Address, helper.Endpoint().HostPublicKey, worker, helperRequest(operationID, clientID))
		result <- err
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		cancel()
		close(blocking.release)
		_ = helper.Close()
		t.Fatal("helper action did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("active SSH session ignored context cancellation")
		}
	case <-time.After(time.Second):
		close(blocking.release)
		_ = helper.Close()
		t.Fatal("active SSH session survived context cancellation")
	}
	close(blocking.release)
	if err := helper.Close(); err != nil {
		t.Fatal(err)
	}
}

type fixedIdentityGenerator struct{ identity Identity }

func (g fixedIdentityGenerator) GenerateInstallationIdentity(_, _ string) (Identity, error) {
	return g.identity, nil
}

func helperRequest(operationID, clientID string) HelperRequest {
	return HelperRequest{
		Schema: HelperSchema, OperationID: operationID, RequestID: uuid.NewString(),
		ClientID: clientID, Action: ActionGenerateIdentity,
	}
}
