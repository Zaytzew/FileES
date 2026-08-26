package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/talk"
)

type updateOnlyClient struct {
	client.Client
	calls   atomic.Int32
	called  chan struct{}
	infoURL string
	infoErr error
}

func (c *updateOnlyClient) GetInfo(context.Context, string) (string, error) {
	if c.infoErr != nil {
		return "", c.infoErr
	}
	return "URL: " + c.infoURL + "\n", nil
}

func (c *updateOnlyClient) Update(context.Context, string) (string, error) {
	c.calls.Add(1)
	select {
	case c.called <- struct{}{}:
	default:
	}
	return "updated", nil
}

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

	live := svc.RecoveryStats()
	want := contract.RecoveryStats{CacheResumed: live.CacheResumed, AlreadyAccepted: live.AlreadyAccepted, CommitBatches: live.CommitBatches}
	if snap.Recovery != want {
		t.Fatalf("recovery stats not wired: snapshot=%#v, service=%#v", snap.Recovery, want)
	}
}

func TestReadOnlyRepoNeverCreatesWatcherOrCommitQueue(t *testing.T) {
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	fileesDir := filepath.Join(wc, ".filees")
	if err := os.MkdirAll(filepath.Join(fileesDir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New(t.TempDir() + "/filees.sock")
	rs := server.RegisterRepoAccess("archive", "svn+ssh://_filees-client@example/archive", wc, "office", contract.AccessReadOnly)
	fake := &updateOnlyClient{called: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runReadOnlyRepo(ctx, config.Repo{ID: "archive", LocalPath: wc, PollInterval: 10 * time.Millisecond}, rs, fake, talk.With("test-readonly"))
	}()

	select {
	case <-fake.called:
	case <-time.After(time.Second):
		t.Fatal("initial svn update not called")
	}
	if err := os.WriteFile(filepath.Join(wc, "local-new.txt"), []byte("local only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "local-modified.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.called:
	case <-time.After(time.Second):
		t.Fatal("periodic svn update not called")
	}

	for _, forbidden := range []string{filepath.Join(fileesDir, "commit_cache"), filepath.Join(fileesDir, "state", "manifest.json")} {
		if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("read-only pipeline created %s: %v", forbidden, err)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read-only loop did not stop")
	}
	if snap := rs.Snapshot(); snap.Access != contract.AccessReadOnly || snap.State != contract.StateStopping || snap.Cycle.Phase != contract.CycleStopped || snap.Cycle.ID < 2 || snap.Cycle.NextTickAt != "" {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestReadOnlyRepoRequestsLocateAfterWorkingCopyMoves(t *testing.T) {
	parent := t.TempDir()
	wc := filepath.Join(parent, "archive")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := ipcserver.New(t.TempDir()+"/filees.sock").RegisterRepoAccess("archive", "svn+ssh://example/archive", wc, "office", contract.AccessReadOnly)
	fake := &updateOnlyClient{called: make(chan struct{}, 2)}
	done := make(chan struct{})
	go func() {
		runReadOnlyRepo(t.Context(), config.Repo{ID: "archive", LocalPath: wc, PollInterval: time.Hour}, rs, fake, talk.With("test-readonly-move"))
		close(done)
	}()
	select {
	case <-fake.called:
	case <-time.After(time.Second):
		t.Fatal("initial update not called")
	}
	moved := filepath.Join(parent, "archive-moved")
	if err := os.Rename(wc, moved); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("read-only pipeline did not notice moved working copy")
	}
	if _, err := os.Stat(wc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only pipeline recreated abandoned working-copy root: %v", err)
	}
	snapshot := rs.Snapshot()
	if snapshot.State != contract.StateInteractionRequired || snapshot.CurrentOperation == nil || *snapshot.CurrentOperation != "working_copy_missing" {
		t.Fatalf("moved read-only working copy snapshot=%+v", snapshot)
	}
}
