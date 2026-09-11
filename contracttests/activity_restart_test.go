package contracttests

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"filees/pkg/activity"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func TestActivityIPCGroupLimitDoesNotCutCommit(t *testing.T) {
	root := t.TempDir()
	j, err := activity.Open(filepath.Join(root, "activity.json"), 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{2, 5, 22} {
		for i := 0; i < n; i++ {
			if err := j.Record(activity.Entry{RepoID: "repo", Path: fmt.Sprintf("%d/%d", n, i), Kind: activity.Added, Stage: activity.Published, Revision: int64(n)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	sock := filepath.Join(root, "ipc.sock")
	s := ipcserver.New(sock)
	s.SetActivitySource(j)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := ipcclient.New(sock, "test").RepoActivity(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int64]int{}
	for _, e := range result.Entries {
		counts[e.Revision]++
	}
	for _, n := range []int64{2, 5, 22} {
		if counts[n] != int(n) {
			t.Fatalf("truncated IPC groups: %v", counts)
		}
	}
	result, err = ipcclient.New(sock, "test").RepoActivity(ctx, 1)
	if err != nil || len(result.Entries) != 22 {
		t.Fatalf("single group: %+v, %v", result, err)
	}
}

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
