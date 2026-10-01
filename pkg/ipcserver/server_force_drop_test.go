package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
)

type detachOnly struct{}

func (detachOnly) Detach(context.Context, string) error { return nil }

type forceDropper struct {
	detachOnly
	dropped []string
	err     error
}

func (f *forceDropper) ForceDrop(_ context.Context, serverID string) error {
	f.dropped = append(f.dropped, serverID)
	return f.err
}

func forceDropRequest(t *testing.T, serverID string) contract.Request {
	t.Helper()
	payload, err := json.Marshal(contract.ServerForceDropPayload{ServerID: serverID})
	if err != nil {
		t.Fatal(err)
	}
	return contract.Request{Protocol: contract.Protocol, RequestID: "r1", Command: contract.CmdServerForceDrop, Payload: payload}
}

func TestServerForceDropIsLocalAndRemovesTheServer(t *testing.T) {
	s := New("")
	s.RegisterActivation(contract.ActivationStatus{ServerID: "lab"})

	s.SetServerDetachService(detachOnly{})
	if resp := s.dispatch(forceDropRequest(t, "lab")); resp.Error == nil || resp.Error.MessageKey != "server.force_drop_unavailable" {
		t.Fatalf("detach-only service advertised force drop: %#v", resp.Error)
	}

	failing := &forceDropper{err: errors.New("dial tcp 127.0.0.1:2222: connection refused")}
	s.SetServerDetachService(failing)
	resp := s.dispatch(forceDropRequest(t, "lab"))
	// A network-shaped local failure must not be relabelled "server
	// unreachable": not reaching the server is the reason this exists.
	if resp.Error == nil || resp.Error.MessageKey != "server.force_drop_failed" || resp.Error.Code != "SERVER-1005" {
		t.Fatalf("failure = %#v", resp.Error)
	}
	if _, active := s.activations["lab"]; !active {
		t.Fatal("failed force drop removed the activation")
	}

	ok := &forceDropper{}
	s.SetServerDetachService(ok)
	resp = s.dispatch(forceDropRequest(t, "lab"))
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var result contract.ServerForceDropResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil || result.ServerID != "lab" {
		t.Fatalf("result = %#v %v", result, err)
	}
	if _, active := s.activations["lab"]; active || len(ok.dropped) != 1 {
		t.Fatalf("activation kept or service not called: %v", ok.dropped)
	}
	if resp := s.dispatch(forceDropRequest(t, "lab")); resp.Error == nil || resp.Error.MessageKey != "server.not_activated" {
		t.Fatalf("second call = %#v", resp.Error)
	}
}
