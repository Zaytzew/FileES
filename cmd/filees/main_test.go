package main

import (
	"testing"
	"time"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
)

func TestWireRepoStatusConnectsCommitServiceToPublicSnapshot(t *testing.T) {
	wc := t.TempDir()
	server := ipcserver.New(t.TempDir() + "/filees.sock")
	rs := server.RegisterRepo("repo", "svn://example/repo", wc)
	svc := &commit.Service{}

	wireRepoStatus(svc, rs)
	if svc.Tickets == nil {
		t.Fatal("ticket maker was not wired")
	}

	svc.OnHeadRevision(42)
	now := time.Now().UTC().Truncate(time.Second)
	svc.OnLastSync(now)
	svc.OnConflicts(3)
	op := "commit"
	svc.OnCurrentOperation(&op)
	svc.OnConnectivity("offline")

	snap := rs.Snapshot()
	if snap.HeadRevision != 42 || snap.Conflicts != 3 || snap.LastSyncAt != now.Format(time.RFC3339) {
		t.Fatalf("snapshot not updated: %#v", snap)
	}
	if snap.CurrentOperation == nil || *snap.CurrentOperation != "commit" {
		t.Fatalf("current operation = %#v", snap.CurrentOperation)
	}
	if snap.Connectivity != contract.ConnOffline || snap.State != contract.StateOffline {
		t.Fatalf("offline state not wired: %#v", snap)
	}

	svc.OnCurrentOperation(nil)
	svc.OnConnectivity("online")
	snap = rs.Snapshot()
	if snap.CurrentOperation != nil || snap.Connectivity != contract.ConnOnline || snap.State != contract.StateActive {
		t.Fatalf("online state not wired: %#v", snap)
	}
}
