package contracttests

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

func TestIPCKeepsIncomingReconciledAndPublishedDistinct(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		return contract.OKResponse(req.RequestID, contract.RepoActivityResult{Entries: []contract.ActivityRecord{
			{RepoID: "repo", Path: "incoming", Stage: "received", Revision: 117},
			{RepoID: "repo", Path: "clean", Stage: "reconciled"},
			{RepoID: "repo", Path: "outgoing", Stage: "published", Revision: 118},
		}})
	})
	result, err := ipcclient.New(sock, "test").RepoActivity(context.Background(), 20)
	if err != nil || len(result.Entries) != 3 {
		t.Fatalf("activity=%+v, %v", result, err)
	}
	for i, want := range []string{"received", "reconciled", "published"} {
		if result.Entries[i].Stage != want {
			t.Fatalf("lost activity direction: %+v", result)
		}
	}
	if result.Entries[1].Revision != 0 {
		t.Fatal("reconciliation invented a commit")
	}
}

func TestIPCRestartRequiredSurvivesNewClientConnection(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		return contract.OKResponse(req.RequestID, contract.UpdateStatus{State: "restart_required", CurrentVersion: "967", AvailableVersion: "970", RestartRequired: true})
	})
	for range 2 {
		result, err := ipcclient.New(sock, "test").UpdateStatus(context.Background())
		if err != nil || result.State != "restart_required" || !result.RestartRequired || result.CurrentVersion != "967" {
			t.Fatalf("update=%+v, %v", result, err)
		}
	}
}
