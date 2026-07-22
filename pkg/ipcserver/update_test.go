package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
)

type updateStub struct {
	status contract.UpdateStatus
	plan   contract.UpdatePlanResult
	apply  contract.UpdateApplyResult
	err    error
}

func (stub updateStub) Status(context.Context) (contract.UpdateStatus, error) {
	return stub.status, stub.err
}
func (stub updateStub) Plan(context.Context) (contract.UpdatePlanResult, error) {
	return stub.plan, stub.err
}
func (stub updateStub) Apply(context.Context) (contract.UpdateApplyResult, error) {
	return stub.apply, stub.err
}

func updateRequest(command string) contract.Request {
	return contract.Request{RequestID: "update-request", Command: command, Payload: json.RawMessage(`{}`)}
}

func TestUpdateCapabilitiesAndStatusAreDynamic(t *testing.T) {
	server := New(t.TempDir() + "/ipc.sock")
	var hello contract.HelloResult
	decodeIPCResult(t, server.dispatch(updateRequest(contract.CmdSystemHello)), &hello)
	if containsCapability(hello.Capabilities, contract.CapUpdateApply) {
		t.Fatal("update capability advertised without an update service")
	}
	if response := server.dispatch(updateRequest(contract.CmdUpdatePlan)); response.Status != contract.StatusError || response.Error.Code != "UPDATE-0001" {
		t.Fatalf("unavailable response = %+v", response)
	}

	server.SetUpdateService(updateStub{status: contract.UpdateStatus{State: "available", CurrentVersion: "1.0", AvailableVersion: "1.1", ReleaseID: "r180"}})
	decodeIPCResult(t, server.dispatch(updateRequest(contract.CmdSystemHello)), &hello)
	for _, capability := range []string{contract.CapUpdateStatus, contract.CapUpdatePlan, contract.CapUpdateApply} {
		if !containsCapability(hello.Capabilities, capability) {
			t.Fatalf("missing capability %q", capability)
		}
	}
	var system contract.SystemStatusResult
	decodeIPCResult(t, server.dispatch(updateRequest(contract.CmdSystemStatus)), &system)
	if system.Update == nil || system.Update.AvailableVersion != "1.1" {
		t.Fatalf("system update = %+v", system.Update)
	}
}

func TestUpdatePlanApplyAndStructuredFailure(t *testing.T) {
	server := New(t.TempDir() + "/ipc.sock")
	server.SetUpdateService(updateStub{
		plan:  contract.UpdatePlanResult{CurrentVersion: "1.0", AvailableVersion: "1.1", Changes: []contract.UpdateChange{{Action: "update", Path: "filees"}}},
		apply: contract.UpdateApplyResult{InstalledVersion: "1.1", RestartRequired: true},
	})
	var plan contract.UpdatePlanResult
	decodeIPCResult(t, server.dispatch(updateRequest(contract.CmdUpdatePlan)), &plan)
	if len(plan.Changes) != 1 || plan.Changes[0].Path != "filees" {
		t.Fatalf("plan = %+v", plan)
	}
	var apply contract.UpdateApplyResult
	decodeIPCResult(t, server.dispatch(updateRequest(contract.CmdUpdateApply)), &apply)
	if apply.InstalledVersion != "1.1" || !apply.RestartRequired {
		t.Fatalf("apply = %+v", apply)
	}
	server.SetUpdateService(updateStub{err: errors.New("signature rejected")})
	if response := server.dispatch(updateRequest(contract.CmdUpdateApply)); response.Status != contract.StatusError || response.Error.Code != "UPDATE-1003" {
		t.Fatalf("apply failure = %+v", response)
	}
}

func decodeIPCResult(t *testing.T, response contract.Response, out any) {
	t.Helper()
	if response.Status != contract.StatusOK {
		t.Fatalf("response = %+v", response)
	}
	if err := json.Unmarshal(response.Result, out); err != nil {
		t.Fatal(err)
	}
}

func containsCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}
