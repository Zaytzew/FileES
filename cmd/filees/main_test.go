package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
)

func TestConfigCheckValidatesWithoutStartingDaemon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	valid := `{"transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"repositories":[]}` + "\n"
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdConfigCheck([]string{"--config", path}); code != 0 {
		t.Fatalf("valid config exit=%d", code)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdConfigCheck([]string{"--config", path}); code != 1 {
		t.Fatalf("invalid config exit=%d", code)
	}
}

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
