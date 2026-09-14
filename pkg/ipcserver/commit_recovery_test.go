package ipcserver

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestCommitRecoveryIsProjectedAndCapabilityGated(t *testing.T) {
	s := New(t.TempDir() + "/daemon.sock")
	rs := s.RegisterRepoAccess("docs", "svn://example/docs", t.TempDir(), "server", contract.AccessReadWrite)
	required := true
	applied := false
	rs.SetCommitRecoveryFuncs(func() bool { return required }, func(context.Context) (*contract.CommitRecoveryPlan, error) {
		return &contract.CommitRecoveryPlan{PlanID: "plan", RepoID: "docs", Choice: contract.CommitRecoveryRetryQueue}, nil
	}, func(_ context.Context, id, choice string) (*contract.CommitRecoveryApplyResult, error) {
		applied = id == "plan" && choice == contract.CommitRecoveryRetryQueue
		return &contract.CommitRecoveryApplyResult{PlanID: id, State: "queued"}, nil
	})
	if !rs.Snapshot().CommitRecoveryRequired {
		t.Fatal("durable commit HOLD is absent from repo projection")
	}
	req := contract.Request{RepoID: "docs", RequestID: "test", Command: contract.CmdRepoCommitRecoveryPlan, Payload: []byte(`{}`)}
	if response := s.handleRepoCommitRecovery(req); response.Status != contract.StatusOK {
		t.Fatalf("plan response=%+v", response)
	}
	req.Command = contract.CmdRepoCommitRecoveryApply
	req.Payload = []byte(`{"plan_id":"plan","choice":"retry_preserved_queue"}`)
	if response := s.handleRepoCommitRecovery(req); response.Status != contract.StatusOK || !applied {
		t.Fatalf("apply response=%+v applied=%v", response, applied)
	}

	rs.SetProjection("svn://example/docs", contract.AccessReadOnly)
	applied = false
	if response := s.handleRepoCommitRecovery(req); response.Status != contract.StatusError || applied {
		t.Fatal("read-only repository accepted commit recovery")
	}
}
