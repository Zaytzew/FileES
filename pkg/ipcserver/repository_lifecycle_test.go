package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

type lifecycleStub struct {
	createCalls, attachCalls, approveCalls, relocateCalls, locateCalls, loadDumpCalls, detachCalls, statusCalls, repairCalls int
	deleteRepository                                                                                                         bool
	statusResult                                                                                                             contract.RepoLifecycleResult
	statusErr                                                                                                                error
	detachResult                                                                                                             contract.RepoLifecycleResult
	detachErr                                                                                                                error
	repairResult                                                                                                             contract.RepoLifecycleResult
	repairErr                                                                                                                error
	repairOperationID, repairServerID, repairRepoID, repairStrategy                                                          string
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
func (stub *lifecycleStub) BeginLocate(serverID, repoID, existingLocalPath string) (contract.RepoLifecycleResult, error) {
	stub.locateCalls++
	return contract.RepoLifecycleResult{OperationID: "op", ServerID: serverID, RepoID: repoID, LocalPath: "/old", PendingLocalPath: existingLocalPath, State: "relocating"}, nil
}
func (stub *lifecycleStub) BeginLoadDump(serverID, repoID string, applyIgnorePolicy bool, keepLastRevisions *int) (contract.RepoLifecycleResult, error) {
	stub.loadDumpCalls++
	return contract.RepoLifecycleResult{OperationID: "op", ServerID: serverID, RepoID: repoID, State: "reconciling"}, nil
}
func (stub *lifecycleStub) BeginAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error) {
	stub.attachCalls++
	state := "unattached"
	if required {
		state = "policy_pending"
	}
	return contract.RepoLifecycleResult{OperationID: "op", ServerID: serverID, RepoID: repoID, LocalPath: localPath, State: state}, nil
}
func (stub *lifecycleStub) BeginDetach(_ context.Context, serverID, repoID string, deleteRepository bool) (contract.RepoLifecycleResult, error) {
	stub.detachCalls++
	stub.deleteRepository = deleteRepository
	if stub.detachResult.OperationID != "" || stub.detachErr != nil {
		return stub.detachResult, stub.detachErr
	}
	state := "detached"
	if deleteRepository {
		state = "deleted"
	}
	return contract.RepoLifecycleResult{OperationID: "op", ServerID: serverID, RepoID: repoID, State: state}, nil
}

func TestDeleteReturnsSuccessAfterDurableServerBoundary(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{
		detachResult: contract.RepoLifecycleResult{
			OperationID: "op", ServerID: "office", RepoID: "repo-1", State: "deleting",
			ServerDeleteCompleted: true, LocalCleanupCompleted: true,
			RetainUntil: "2026-09-22T17:38:06Z", LastError: "recovery ticket unsupported",
		},
		detachErr: errors.New("recovery ticket unsupported"),
	}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterActivation(contract.ActivationStatus{
		ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "realm-1",
		CanCreateRepositories: true,
	})
	server.RegisterRepoAccess("repo-1", "svn://example/repo-1", "/wc/repo-1", "office", "rw")
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "realm-1", "optional", true)

	request := lifecycleRequest(contract.CmdRepoDelete, contract.RepoDetachPayload{ServerID: "office", RepoID: "repo-1"})
	response := server.dispatch(request)
	if response.Status != contract.StatusOK {
		t.Fatalf("durable server deletion reported as failure: %+v", response.Error)
	}
	var result contract.RepoLifecycleResult
	if err := contract.DecodeResult(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.ServerDeleteCompleted || !result.LocalCleanupCompleted || result.State != "deleting" {
		t.Fatalf("result lost pending recovery state: %+v", result)
	}
}

func (stub *lifecycleStub) Status(operationID string) (contract.RepoLifecycleResult, error) {
	stub.statusCalls++
	return stub.statusResult, stub.statusErr
}

func (stub *lifecycleStub) Repair(_ context.Context, operationID, serverID, repoID, strategy string) (contract.RepoLifecycleResult, error) {
	stub.repairCalls++
	stub.repairOperationID, stub.repairServerID, stub.repairRepoID, stub.repairStrategy = operationID, serverID, repoID, strategy
	if stub.repairResult.OperationID != "" || stub.repairErr != nil {
		return stub.repairResult, stub.repairErr
	}
	return contract.RepoLifecycleResult{OperationID: operationID, ServerID: serverID, RepoID: repoID, State: "repository_created"}, nil
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

func TestLifecycleRepairUsesProjectedOperationFence(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	state := server.RegisterProjectedRepo("repo-1", "Docs", "svn+ssh://example/repo-1", "office", "rw", "interaction_required", false)
	state.SetLifecycleRepairMetadata("op-current", "initial import failed", true, true)

	stale := lifecycleRequest(contract.CmdRepoLifecycleRepair, contract.RepoLifecycleRepairPayload{OperationID: "op-stale", ServerID: "office", RepoID: "repo-1", Strategy: "retry"})
	if response := server.dispatch(stale); response.Status == contract.StatusOK {
		t.Fatal("stale repair operation was accepted")
	}
	if stub.repairCalls != 0 {
		t.Fatalf("stale repair reached service: %d", stub.repairCalls)
	}

	current := lifecycleRequest(contract.CmdRepoLifecycleRepair, contract.RepoLifecycleRepairPayload{OperationID: "op-current", ServerID: "office", RepoID: "repo-1", Strategy: "retry"})
	if response := server.dispatch(current); response.Status != contract.StatusOK {
		t.Fatalf("current repair rejected: %+v", response.Error)
	}
	if stub.repairCalls != 1 || stub.repairOperationID != "op-current" || stub.repairServerID != "office" || stub.repairRepoID != "repo-1" || stub.repairStrategy != "retry" {
		t.Fatalf("repair call lost identity: %+v", stub)
	}
	inProgress := state.Summary()
	if inProgress.LifecycleOperationID != "op-current" || inProgress.LifecycleError != "" || inProgress.CanRetryLifecycle || inProgress.CanAbandonLifecycle {
		t.Fatalf("accepted retry did not enter projected operation fence: %+v", inProgress)
	}
}

func TestLifecycleRepairOnlyAllowsProjectedStrategies(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	state := server.RegisterProjectedRepo("repo-1", "Docs", "svn+ssh://example/repo-1", "office", "rw", "interaction_required", false)
	state.SetLifecycleRepairMetadata("op-current", "detach failed", true, false)

	abandon := lifecycleRequest(contract.CmdRepoLifecycleRepair, contract.RepoLifecycleRepairPayload{OperationID: "op-current", ServerID: "office", RepoID: "repo-1", Strategy: "abandon"})
	if response := server.dispatch(abandon); response.Status == contract.StatusOK {
		t.Fatal("unadvertised abandon strategy was accepted")
	}
	if stub.repairCalls != 0 {
		t.Fatalf("forbidden strategy reached service: %d", stub.repairCalls)
	}
}

func TestLifecycleRepairFailurePreservesDiagnosticDetail(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{repairErr: errors.New("working-copy URL cannot be verified")}
	server.SetRepositoryLifecycleService(stub)
	state := server.RegisterProjectedRepo("repo-1", "Docs", "svn+ssh://example/repo-1", "office", "rw", "interaction_required", false)
	state.SetLifecycleRepairMetadata("op-current", "initial import failed", false, true)

	request := lifecycleRequest(contract.CmdRepoLifecycleRepair, contract.RepoLifecycleRepairPayload{OperationID: "op-current", ServerID: "office", RepoID: "repo-1", Strategy: "abandon"})
	response := server.dispatch(request)
	if response.Status != contract.StatusError || response.Error == nil {
		t.Fatalf("repair failure response=%+v", response)
	}
	if response.Error.MessageKey != "repo.lifecycle_repair_failed" || response.Error.Details["detail"] != stub.repairErr.Error() {
		t.Fatalf("repair diagnostic was lost: %+v", response.Error)
	}
	restored := state.Summary()
	if restored.LifecycleOperationID != "op-current" || restored.LifecycleError != "initial import failed" || restored.CanRetryLifecycle || !restored.CanAbandonLifecycle {
		t.Fatalf("synchronous failure lost the prior repair offer: %+v", restored)
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

func TestLocateRequiresAttachedRepositoryAndUsesDistinctCommand(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn+ssh://_filees-client@example/repo", "primary", "r", "active", "owner", "optional", false)
	req := lifecycleRequest(contract.CmdRepoLocate, contract.RepoLocatePayload{ServerID: "primary", RepoID: "repo-1", ExistingLocalPath: "/moved"})
	if response := server.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("unattached repository locate accepted")
	}
	server.RegisterRepoAccess("repo-1", "svn+ssh://_filees-client@example/repo", "/old", "primary", "r")
	if response := server.dispatch(req); response.Status != contract.StatusOK {
		t.Fatalf("attached locate rejected: %+v", response.Error)
	}
	if stub.locateCalls != 1 || stub.relocateCalls != 0 {
		t.Fatalf("locate calls=%d relocation calls=%d", stub.locateCalls, stub.relocateCalls)
	}
}

func TestDetachAndDeleteAreDistinctAndRequiredPolicyIsEnforced(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterActivation(contract.ActivationStatus{
		ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "realm-1",
		CanCreateRepositories: true,
	})
	server.RegisterRepoAccess("repo-1", "svn://example/repo-1", "/wc/repo-1", "office", "rw")
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "realm-1", "optional", true)

	detach := lifecycleRequest(contract.CmdRepoDetach, contract.RepoDetachPayload{ServerID: "office", RepoID: "repo-1"})
	if response := server.dispatch(detach); response.Status != contract.StatusOK {
		t.Fatalf("local detach rejected: %+v", response.Error)
	}
	if stub.detachCalls != 1 || stub.deleteRepository {
		t.Fatalf("local detach routed incorrectly: calls=%d delete=%v", stub.detachCalls, stub.deleteRepository)
	}

	deleteRequest := lifecycleRequest(contract.CmdRepoDelete, contract.RepoDetachPayload{ServerID: "office", RepoID: "repo-1"})
	if response := server.dispatch(deleteRequest); response.Status != contract.StatusOK {
		t.Fatalf("permanent delete rejected: %+v", response.Error)
	}
	if stub.detachCalls != 2 || !stub.deleteRepository {
		t.Fatalf("permanent delete routed incorrectly: calls=%d delete=%v", stub.detachCalls, stub.deleteRepository)
	}

	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "realm-1", "required", true)
	if response := server.dispatch(detach); response.Status == contract.StatusOK {
		t.Fatal("required repository accepted local detach")
	}
	if response := server.dispatch(deleteRequest); response.Status == contract.StatusOK {
		t.Fatal("required repository accepted permanent delete")
	}
}

func TestDeleteRequiresAuthenticatedOwningRealm(t *testing.T) {
	server := New("unused")
	stub := &lifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterActivation(contract.ActivationStatus{
		ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "caller",
		CanCreateRepositories: true,
	})
	server.RegisterRepoAccess("repo-1", "svn://example/repo-1", "/wc/repo-1", "office", "rw")
	server.RegisterProjectedRepoPolicy("repo-1", "Shared", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)
	request := lifecycleRequest(contract.CmdRepoDelete, contract.RepoDetachPayload{ServerID: "office", RepoID: "repo-1"})
	if response := server.dispatch(request); response.Status == contract.StatusOK {
		t.Fatal("foreign realm deleted repository")
	}
	if stub.detachCalls != 0 {
		t.Fatalf("forbidden delete reached lifecycle service: %d", stub.detachCalls)
	}
}
