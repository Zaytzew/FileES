package clientactivation

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"filees/internal/gui/actions"
	contract "filees/pkg/contract/v1"
	"filees/pkg/onboarding"
)

type clientStub struct {
	begin   contract.ActivationBeginPayload
	finish  contract.ActivationFinishPayload
	pending contract.ActivationPendingPayload
	resume  contract.ActivationResumePayload
}

func (stub *clientStub) ActivationBegin(_ context.Context, payload contract.ActivationBeginPayload) (*contract.ActivationCommandResult, error) {
	stub.begin = payload
	return &contract.ActivationCommandResult{}, nil
}
func (stub *clientStub) ActivationFinish(_ context.Context, payload contract.ActivationFinishPayload) (*contract.ActivationCommandResult, error) {
	stub.finish = payload
	stub.finish.OTP = append(contract.Secret(nil), payload.OTP...)
	return &contract.ActivationCommandResult{}, nil
}
func (stub *clientStub) ActivationPending(_ context.Context, payload contract.ActivationPendingPayload) (*contract.ActivationPendingResult, error) {
	stub.pending = payload
	return &contract.ActivationPendingResult{Targets: []contract.ActivationTarget{{ServerID: "spot", Address: "spot:2223"}}}, nil
}
func (stub *clientStub) ActivationResume(_ context.Context, payload contract.ActivationResumePayload) (*contract.ActivationCommandResult, error) {
	stub.resume = payload
	return &contract.ActivationCommandResult{}, nil
}

func TestActivatorUsesInvitationAndOneStateRootForEveryPhase(t *testing.T) {
	client := &clientStub{}
	root := t.TempDir()
	activator := New(client, root)
	wire, err := onboarding.EncodeInvitation(onboarding.Invitation{
		Schema: onboarding.InvitationSchema, Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		ServerID: "spot", ServerAddress: "spot:2223", KnownHost: "spot ssh-ed25519 AAAA",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := activator.Begin(t.Context(), wire)
	if err != nil || target.ServerID != "spot" || target.Address != "spot:2223" {
		t.Fatalf("Begin() target=%+v err=%v", target, err)
	}
	wantHosts := filepath.Join(root, "spot", "known_hosts")
	if client.begin.StateRoot != root || client.begin.KnownHostsPath != wantHosts || client.begin.Invitation != wire {
		t.Fatalf("begin payload = %+v", client.begin)
	}
	if err := activator.Finish(t.Context(), target, []byte("123456")); err != nil {
		t.Fatal(err)
	}
	if client.finish.KnownHostsPath != wantHosts || string(client.finish.OTP) != "123456" {
		t.Fatalf("finish payload = %+v", client.finish)
	}
	if err := activator.Resume(t.Context(), target); err != nil || client.resume.KnownHostsPath != wantHosts {
		t.Fatalf("resume payload = %+v err=%v", client.resume, err)
	}
	pending, err := activator.Pending(t.Context())
	if err != nil || len(pending) != 1 || pending[0] != (actions.ActivationTarget{ServerID: "spot", Address: "spot:2223"}) || client.pending.StateRoot != root {
		t.Fatalf("Pending()=%+v payload=%+v err=%v", pending, client.pending, err)
	}
}
