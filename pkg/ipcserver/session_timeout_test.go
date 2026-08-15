package ipcserver

import (
	"context"
	"encoding/json"
	"testing"

	contract "filees/pkg/contract/v1"
)

type fakeSessionTimeout struct{ minutes int }

func (f *fakeSessionTimeout) SetSessionTimeout(_ context.Context, _ string, minutes int) (int, error) {
	f.minutes = minutes
	return minutes, nil
}

func TestHandleServerSetSessionTimeout(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	fake := &fakeSessionTimeout{}
	server.SetSessionTimeoutService(fake)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", DisplayName: "office"})
	payload, _ := json.Marshal(contract.ServerSetSessionTimeoutPayload{ServerID: "office", Minutes: 90})
	resp := server.dispatch(contract.Request{Protocol: contract.Protocol, RequestID: "1", ClientID: "gui", Command: contract.CmdServerSetSessionTimeout, Payload: payload})
	if resp.Status != contract.StatusOK {
		t.Fatalf("resp=%+v", resp.Error)
	}
	if fake.minutes != 90 {
		t.Fatalf("saved=%d", fake.minutes)
	}
	var result contract.ServerSetSessionTimeoutResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil || result.Minutes != 90 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
