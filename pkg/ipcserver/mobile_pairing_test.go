package ipcserver

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
)

type mobilePairingStub struct {
	calls  int
	result contract.MobilePairingBeginResult
	err    error
}

func (stub *mobilePairingStub) Begin(ctx context.Context, serverID string) (contract.MobilePairingBeginResult, error) {
	stub.calls++
	return stub.result, stub.err
}

func TestMobilePairingBeginRequiresConfiguredServiceAndActivatedServer(t *testing.T) {
	server := New("unused")
	req := lifecycleRequest(contract.CmdMobilePairingBegin, contract.MobilePairingBeginPayload{ServerID: "biuro"})

	// No service configured at all - fails closed, never dereferences a nil service.
	if response := server.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("mobile pairing accepted with no service configured")
	}

	stub := &mobilePairingStub{result: contract.MobilePairingBeginResult{Token: "OTP-TOKEN", Address: "biuro.example.net:22", HostPublicKey: "ssh-ed25519 AAAA..."}}
	server.SetMobilePairingService(stub)

	// Service configured but "biuro" is not (yet) a registered activation.
	if response := server.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("mobile pairing accepted for an unactivated server")
	}
	if stub.calls != 0 {
		t.Fatalf("service called before activation gate passed: calls=%d", stub.calls)
	}

	server.RegisterActivation(contract.ActivationStatus{ServerID: "biuro", ClientRole: contract.ClientRoleNormal})
	response := server.dispatch(req)
	if response.Status != contract.StatusOK {
		t.Fatalf("activated server rejected: %+v", response.Error)
	}
	if stub.calls != 1 {
		t.Fatalf("service calls=%d, want 1", stub.calls)
	}
	var result contract.MobilePairingBeginResult
	if err := contract.DecodeResult(response.Result, &result); err != nil || result != stub.result {
		t.Fatalf("result=%+v err=%v, want %+v", result, err, stub.result)
	}
}

func TestMobilePairingBeginPropagatesServiceFailure(t *testing.T) {
	server := New("unused")
	server.RegisterActivation(contract.ActivationStatus{ServerID: "biuro", ClientRole: contract.ClientRoleNormal})
	server.SetMobilePairingService(&mobilePairingStub{err: errTestMobilePairing})
	req := lifecycleRequest(contract.CmdMobilePairingBegin, contract.MobilePairingBeginPayload{ServerID: "biuro"})
	if response := server.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("mobile pairing accepted despite service failure")
	}
}

var errTestMobilePairing = &testMobilePairingError{}

type testMobilePairingError struct{}

func (*testMobilePairingError) Error() string { return "control exchange failed" }
