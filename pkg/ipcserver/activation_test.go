package ipcserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

func TestRegisterActivationEmitsRefreshEvent(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	events := make(chan contract.Event, 1)
	server.addSub(events)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", DisplayName: "office", Address: "filees.example.net", SSHPort: 22})
	select {
	case event := <-events:
		if event.Type != contract.EvActivationChanged || event.RepoID != "" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("activation refresh event not emitted")
	}
}

type fakeActivationService struct {
	began, finished bool
	lastOTP         contract.Secret
}

func (service *fakeActivationService) Begin(_ context.Context, payload contract.ActivationBeginPayload) (contract.ActivationCommandResult, error) {
	service.began = payload.Email == "user@example.net" && payload.ServerAddress == "filees.example.net:22"
	return contract.ActivationCommandResult{ServerID: payload.ServerID, State: "otp_required"}, nil
}

func (service *fakeActivationService) Finish(_ context.Context, payload contract.ActivationFinishPayload) (contract.ActivationCommandResult, error) {
	service.finished = string(payload.OTP) == "OTP-CODE" && payload.RemotePort == 42000
	service.lastOTP = payload.OTP
	return contract.ActivationCommandResult{ServerID: payload.ServerID, State: "active"}, nil
}

func activationRequest(t *testing.T, command string, payload any) contract.Request {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return contract.Request{Protocol: contract.Protocol, RequestID: "request", ClientID: "gui", Command: command, Payload: raw}
}

func TestActivationCommandsAreExecutedByConfiguredDaemonService(t *testing.T) {
	server := New(t.TempDir() + "/sock")
	service := &fakeActivationService{}
	server.SetActivationService(service)
	begin := server.dispatch(activationRequest(t, contract.CmdActivationBegin, contract.ActivationBeginPayload{ServerID: "office", ServerAddress: "filees.example.net:22", Email: "user@example.net"}))
	if begin.Status != contract.StatusOK || !service.began {
		t.Fatalf("begin=%+v called=%v", begin, service.began)
	}
	finishRequest := activationRequest(t, contract.CmdActivationFinish, contract.ActivationFinishPayload{ServerID: "office", RemotePort: 42000, OTP: contract.Secret("OTP-CODE")})
	raw := finishRequest.Payload
	finish := server.dispatch(finishRequest)
	if finish.Status != contract.StatusOK || !service.finished {
		t.Fatalf("finish=%+v called=%v", finish, service.finished)
	}
	for _, b := range raw {
		if b != 0 {
			t.Fatal("activation finish request retained raw OTP payload")
		}
	}
	for _, b := range service.lastOTP {
		if b != 0 {
			t.Fatal("activation finish retained decoded OTP bytes")
		}
	}
}

func TestActivationCommandFailsClosedWithoutDaemonService(t *testing.T) {
	server := New(t.TempDir() + "/sock")
	response := server.dispatch(activationRequest(t, contract.CmdActivationBegin, contract.ActivationBeginPayload{}))
	if response.Status != contract.StatusError || response.Error == nil || response.Error.Code != "ACTIVATION-0001" {
		t.Fatalf("response=%+v", response)
	}
}

func TestActivationRepositoryReadinessPreservesProfileMetadata(t *testing.T) {
	server := New("unused")
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", DisplayName: "Office", ClientID: "client", CanCreateRepositories: true})
	server.SetActivationRepositoryReadiness("office", false, 2)
	statuses := server.allActivations()
	if len(statuses) != 1 || statuses[0].DisplayName != "Office" || !statuses[0].CanCreateRepositories || statuses[0].RepositoriesReady || statuses[0].PendingRequiredRepos != 2 {
		t.Fatalf("activation=%+v", statuses)
	}
	server.SetActivationRepositoryReadiness("office", true, 0)
	if got := server.allActivations()[0]; !got.RepositoriesReady || got.PendingRequiredRepos != 0 {
		t.Fatalf("ready activation=%+v", got)
	}
}
