package ipcserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
)

type pendingAnchorBatch struct{ err error }

func (p pendingAnchorBatch) Plan(context.Context, contract.RepoAnchorPlanPayload) (contract.RepoAnchorPlan, error) {
	return contract.RepoAnchorPlan{}, p.err
}
func (p pendingAnchorBatch) Execute(context.Context, contract.RepoAnchorBatchPayload) (contract.RepoAnchorBatchResult, error) {
	return contract.RepoAnchorBatchResult{}, p.err
}

func TestAnchorBatchReportsPendingRelease(t *testing.T) {
	for _, action := range []string{"download", "release"} {
		for _, execute := range []bool{false, true} {
			s := New(filepath.Join(t.TempDir(), "daemon.sock"))
			s.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
			s.RegisterRepoAccess("repo", "file:///fixture/repo", t.TempDir(), "server", contract.AccessReadWrite)
			s.SetAnchorBatch(pendingAnchorBatch{commit.ErrFileReleasePending})
			payload, _ := json.Marshal(contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: ".", Action: action}, Fingerprint: strings.Repeat("a", 64)})
			got := s.handleAnchorBatch(contract.Request{RequestID: "test", Payload: payload}, execute)
			raw, _ := json.Marshal(got)
			if !strings.Contains(string(raw), "HEAD-2016") || !strings.Contains(string(raw), "head.release_pending") {
				t.Fatalf("%s execute=%v: %s", action, execute, raw)
			}
		}
	}
}

func TestPendingReleaseOverlayDisappearsOnlyWithFence(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "daemon.sock"))
	wc := t.TempDir()
	rs := s.RegisterRepoAccess("repo", "file:///fixture/repo", wc, "server", contract.AccessReadWrite)
	rs.SetState(contract.StateActive)
	stateDir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	fence := filepath.Join(stateDir, commit.FileReleasePendingName)
	// Even malformed evidence must remain fenced; projection never parses it.
	if err := os.WriteFile(fence, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if rs.Summary().State != contract.StateDegraded || rs.Snapshot().State != contract.StateDegraded {
		t.Fatal("pending release shown as active")
	}
	rs.SetState(contract.StateRevoked)
	if rs.Summary().State != contract.StateRevoked {
		t.Fatal("release overlay hid revocation")
	}
	rs.SetState(contract.StateActive)
	if err := os.Remove(fence); err != nil {
		t.Fatal(err)
	}
	if rs.Summary().State != contract.StateActive || rs.Snapshot().State != contract.StateActive {
		t.Fatal("historical release failure remained active")
	}
}
