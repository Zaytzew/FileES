package ipcserver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

type lifecycleStub struct {
	createCalls, attachCalls, approveCalls, relocateCalls, statusCalls int
	statusResult                                                      contract.RepoLifecycleResult
	statusErr                                                         error
}

func (stub *lifecycleStub) BeginCreate(serverID, displayName, localPath string) (contract.RepoLifecycleResult, error) {
	stub.createCalls++
	return contract.RepoLifecycleResult{OperationID: "op", ServerID: serverID, LocalPath: localPath, State: "request_pending"}, nil
}
func (stub *lifecycleStub) ApproveAttach(operationID, serverID, repoID, repoURL, access string) (contract.RepoLifecycleResult, error) {
	stub.approveCalls++
	return contract.RepoLifecycleResult{OperationID: operationID, ServerID: serverID, RepoID: repoID, State: "attaching"}, nil
}
func (stub *lifecycleStub) BeginRelocate(serverID, repoID, newLocalPath string) (contract.RepoLifecycleResult, error) {
	stub.relocateCalls++
	return contract.RepoLifecycleResult{OperationID: "op", ServerID: serverID, RepoID: repoID, LocalPath: "/old", PendingLocalPath: newLocalPath, State: "relocating"}, nil
}
func (stub *lifecycleStub) BeginAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error) {
	stub.attachCalls++
	state := "unattached"
	if required {
		state = "policy_pending"
	}
	return contract.RepoLifecycleResult{OperationID: "op", ServerID: serverID, RepoID: repoID, LocalPath: localPath, State: state}, nil
}

func (stub *lifecycleStub) Status(operationID string) (contract.RepoLifecycleResult, error) {
	stub.statusCalls++
	return stub.statusResult, stub.statusErr
}

func lifecycleRequest(command string, payload any) contract.Request {
	raw, _ := json.Marshal(payload)
	return contract.Request{RequestID: "request", Command: command, Payload: raw}
}

func TestCreateRequestRequiresProjectedCapability(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	req := lifecycleRequest(contract.CmdRepoCreateRequest, contract.RepoCreateRequestPayload{ServerID: "managed", DisplayName: "Docs", LocalPath: "/data/docs"})
	server.RegisterActivation(contract.ActivationStatus{ServerID: "managed", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: false})
	if response := server.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("managed client create accepted")
	}
	server.RegisterActivation(contract.ActivationStatus{ServerID: "managed", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true})
	if response := server.dispatch(req); response.Status != contract.StatusOK {
		t.Fatalf("capable client rejected: %+v", response.Error)
	}
	if stub.createCalls != 1 {
		t.Fatalf("create calls=%d", stub.createCalls)
	}
}

func TestAttachIntentUsesProjectedRequiredPolicy(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo", "primary", "r", "active", "owner", "required", false)
	req := lifecycleRequest(contract.CmdRepoAttachIntent, contract.RepoAttachIntentPayload{ServerID: "primary", RepoID: "repo-1", LocalPath: "/data/share"})
	response := server.dispatch(req)
	if response.Status != contract.StatusOK {
		t.Fatalf("attach rejected: %+v", response.Error)
	}
	if stub.attachCalls != 1 {
		t.Fatalf("attach calls=%d", stub.attachCalls)
	}
}

func TestAttachApprovalUsesCurrentProjectedAuthority(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn+ssh://_filees-client@example/repo", "primary", "r", "active", "owner", "optional", false)
	req := lifecycleRequest(contract.CmdRepoAttachApprove, contract.RepoAttachApprovePayload{OperationID: "op", ServerID: "primary", RepoID: "repo-1"})
	if response := server.dispatch(req); response.Status != contract.StatusOK {
		t.Fatalf("approval rejected: %+v", response.Error)
	}
	if stub.approveCalls != 1 {
		t.Fatalf("approval calls=%d", stub.approveCalls)
	}
}

func TestLifecycleStatusReturnsCurrentStateAndError(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{statusResult: contract.RepoLifecycleResult{
		OperationID: "op-1", ServerID: "primary", LocalPath: "/data/skany", State: "error",
		LastError: "STORAGE_INSUFFICIENT: server storage requires 187424908 bytes, 118276096 available",
	}}
	server.SetRepositoryLifecycleService(stub)
	req := lifecycleRequest(contract.CmdRepoLifecycleStatus, contract.RepoLifecycleStatusPayload{OperationID: "op-1"})
	response := server.dispatch(req)
	if response.Status != contract.StatusOK {
		t.Fatalf("status query rejected: %+v", response.Error)
	}
	var result contract.RepoLifecycleResult
	if err := contract.DecodeResult(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.State != "error" || !strings.Contains(result.LastError, "STORAGE_INSUFFICIENT") {
		t.Fatalf("status result = %+v", result)
	}
	if stub.statusCalls != 1 {
		t.Fatalf("status calls=%d", stub.statusCalls)
	}
}

func TestLifecycleStatusRejectsUnknownOperation(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{statusErr: os.ErrNotExist}
	server.SetRepositoryLifecycleService(stub)
	req := lifecycleRequest(contract.CmdRepoLifecycleStatus, contract.RepoLifecycleStatusPayload{OperationID: "missing"})
	if response := server.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("unknown operation ID accepted")
	}
}

func TestRelocationRequiresAttachedRepository(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn+ssh://_filees-client@example/repo", "primary", "r", "active", "owner", "optional", false)
	req := lifecycleRequest(contract.CmdRepoRelocate, contract.RepoRelocatePayload{ServerID: "primary", RepoID: "repo-1", NewLocalPath: "/new"})
	if response := server.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("unattached repository relocation accepted")
	}
	server.RegisterRepoAccess("repo-1", "svn+ssh://_filees-client@example/repo", "/old", "primary", "r")
	if response := server.dispatch(req); response.Status != contract.StatusOK {
		t.Fatalf("attached relocation rejected: %+v", response.Error)
	}
	if stub.relocateCalls != 1 {
		t.Fatalf("relocation calls=%d", stub.relocateCalls)
	}
}
