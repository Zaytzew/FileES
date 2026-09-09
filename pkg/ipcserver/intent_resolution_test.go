package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"os"
	"path/filepath"
	"testing"
)

func TestIntentHandlerAuthorizationAndClosedDecision(t *testing.T) {
	s := New(t.TempDir())
	rs := s.RegisterRepo("docs", "svn://example/docs", t.TempDir())
	applied := false
	rs.SetIntentFuncs(func(context.Context) (*contract.IntentPlan, error) {
		return &contract.IntentPlan{PlanID: "opaque", RepoID: "docs", Choice: contract.IntentDeleteAdd}, nil
	}, func(_ context.Context, id, choice string) (*contract.IntentApplyResult, error) {
		applied = true
		if id != "opaque" || choice != contract.IntentDeleteAdd {
			t.Fatal("changed decision")
		}
		return &contract.IntentApplyResult{PlanID: id, State: "queued"}, nil
	})
	req := contract.Request{RepoID: "docs", RequestID: "test", Command: contract.CmdRepoIntentPlan, Payload: []byte(`{}`)}
	if resp := s.handleRepoIntent(req); resp.Status != contract.StatusOK {
		t.Fatalf("plan=%+v", resp)
	}
	req.Command = contract.CmdRepoIntentApply
	req.Payload = []byte(`{"plan_id":"opaque","choice":"delete_add"}`)
	if resp := s.handleRepoIntent(req); resp.Status != contract.StatusOK || !applied {
		t.Fatalf("apply=%+v", resp)
	}
	applied = false
	rs.SetProjection("svn://example/docs", "ro")
	if resp := s.handleRepoIntent(req); resp.Status != contract.StatusError || applied {
		t.Fatal("read-only applied")
	}
	rs.SetProjection("svn://example/docs", "rw")
	rs.SetIntentFuncs(nil, nil)
	if resp := s.handleRepoIntent(req); resp.Status != contract.StatusError {
		t.Fatal("detached callback accepted")
	}
}

func TestPendingStatsReadsResolutionEnvelope(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte(`{"schema":"filees.commit-cache/v2","entries":[{"op":"added"},{"op":"deleted"}],"intent_receipts":[{"plan_id":"x"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	got := readPendingStats(p)
	if got.Added != 1 || got.Deleted != 1 || got.RenameUncertain != 0 {
		t.Fatalf("projection=%+v", got)
	}
}
