package ipcserver

import (
	"context"
	"path/filepath"
	"testing"

	contract "filees/pkg/contract/v1"
)

type whaleStub struct {
	operations []contract.WhaleOperation
	putCalls   int
}

func (s *whaleStub) List(context.Context) ([]contract.WhaleOperation, error) {
	return s.operations, nil
}
func (s *whaleStub) Get(_ context.Context, _ string) (contract.WhaleOperation, error) {
	return s.operations[0], nil
}
func (s *whaleStub) BeginPut(_ context.Context, payload contract.WhalePutBeginPayload) (contract.WhaleOperation, error) {
	s.putCalls++
	return contract.WhaleOperation{OperationID: "op", ServerID: payload.ServerID, State: "preparing", Identity: contract.WhaleIdentity{LogicalRepoID: payload.RepoID, LogicalPath: payload.LogicalPath}}, nil
}
func (s *whaleStub) BeginGet(context.Context, contract.WhaleGetBeginPayload) (contract.WhaleOperation, error) {
	return s.operations[0], nil
}
func (s *whaleStub) ConfirmGet(context.Context, string) (contract.WhaleOperation, error) {
	return s.operations[0], nil
}
func (s *whaleStub) Retry(context.Context, string) (contract.WhaleOperation, error) {
	return s.operations[0], nil
}
func (s *whaleStub) Cancel(context.Context, string, bool) (contract.WhaleOperation, error) {
	return s.operations[0], nil
}

func TestWhaleCapabilitiesAppearOnlyWithActor(t *testing.T) {
	server := New("unused")
	if hasCapability(server.capabilities(), contract.CapWhalePutBegin) {
		t.Fatal("Whale capability advertised without actor")
	}
	server.SetWhaleService(&whaleStub{})
	for _, capability := range []string{contract.CapWhaleList, contract.CapWhaleGet, contract.CapWhalePutBegin, contract.CapWhaleGetBegin, contract.CapWhaleGetConfirm, contract.CapWhaleRetry, contract.CapWhaleCancel} {
		if !hasCapability(server.capabilities(), capability) {
			t.Fatalf("missing capability %s", capability)
		}
	}
}

func TestWhalePutIntentRequiresProjectedRepoServerMatch(t *testing.T) {
	server := New("unused")
	stub := &whaleStub{}
	server.SetWhaleService(stub)
	server.RegisterProjectedRepo("repo-1", "Repo", "url", "office", "rw", "active", true)
	payload := contract.WhalePutBeginPayload{ServerID: "wrong", RepoID: "repo-1", LogicalPath: "media/a.bin", SourcePath: filepath.Join(t.TempDir(), "a.bin")}
	if response := server.dispatch(lifecycleRequest(contract.CmdWhalePutBegin, payload)); response.Status == contract.StatusOK {
		t.Fatal("Whale intent crossed server/repository projection")
	}
	if stub.putCalls != 0 {
		t.Fatal("actor called before repository gate")
	}
	payload.ServerID = "office"
	response := server.dispatch(lifecycleRequest(contract.CmdWhalePutBegin, payload))
	if response.Status != contract.StatusOK || stub.putCalls != 1 {
		t.Fatalf("response=%+v calls=%d", response, stub.putCalls)
	}
	var operation contract.WhaleOperation
	if err := contract.DecodeResult(response.Result, &operation); err != nil || operation.State != "preparing" {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}

func hasCapability(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
