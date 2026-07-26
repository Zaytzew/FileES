package ipcserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestReadOnlyRepoRejectsLockAndUnlockBeforeBackend(t *testing.T) {
	rs := New(t.TempDir()+"/daemon.sock").RegisterRepoAccess("archive", "svn+ssh://host/archive", t.TempDir(), "server", contract.AccessReadOnly)
	calls := 0
	rs.SetLockFuncs(func(context.Context, []string) (string, error) { calls++; return "", nil }, func(context.Context, []string) (string, error) { calls++; return "", nil })
	if _, err := rs.Lock(t.Context(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "REPO_READ_ONLY") {
		t.Fatalf("lock error=%v", err)
	}
	if _, err := rs.Unlock(t.Context(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "REPO_READ_ONLY") {
		t.Fatalf("unlock error=%v", err)
	}
	if calls != 0 {
		t.Fatalf("SVN backend called %d times", calls)
	}
}

func TestReconcileProjectedReposRemovesOnlyMissingIPCKnowledge(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	events := make(chan contract.Event, 1)
	server.addSub(events)
	wc := t.TempDir()
	marker := filepath.Join(wc, "keep-me")
	if err := os.WriteFile(marker, []byte("local data"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.RegisterRepoAccess("gone", "svn+ssh://host/gone", wc, "office", contract.AccessReadWrite)
	server.RegisterProjectedRepo("other", "Other", "svn+ssh://host/other", "home", contract.AccessReadOnly, "active", false)
	server.ReconcileProjectedRepos("office", []ProjectedRepo{{ID: "kept", DisplayName: "Kept", URL: "svn+ssh://host/kept", Access: contract.AccessReadOnly, State: "active"}})
	if server.repoByID("gone") != nil {
		t.Fatal("repository omitted by authoritative projection remains in IPC")
	}
	if server.repoByID("kept") == nil || server.repoByID("other") == nil {
		t.Fatal("reconcile removed present repository or another server's repository")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "local data" {
		t.Fatalf("local working copy was touched: data=%q err=%v", data, err)
	}
	select {
	case event := <-events:
		if event.Type != contract.EvProjectionChanged || event.RepoID != "" {
			t.Fatalf("event=%+v", event)
		}
	default:
		t.Fatal("projection removal did not request a full GUI refresh")
	}
}

func TestProjectedUnattachedRepoIsPresentationOnly(t *testing.T) {
	rs := New(t.TempDir()+"/daemon.sock").RegisterProjectedRepo("repo-id", "Shared documents", "svn+ssh://_filees-client@example/repo", "office", contract.AccessReadOnly, "active", false)
	summary := rs.Summary()
	if summary.DisplayName != "Shared documents" || summary.Attached || summary.LocalPath != "" || summary.State != contract.StateUnattached {
		t.Fatalf("summary=%+v", summary)
	}
	snapshot := rs.Snapshot()
	if snapshot.Attached || snapshot.State != contract.StateUnattached || snapshot.LocalRevision != 0 || snapshot.Pending != (contract.PendingStats{}) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, err := rs.Lock(t.Context(), []string{"/tmp/file"}); err == nil {
		t.Fatal("unattached repository accepted lock")
	}
}

func TestMarkRepoDetachedClearsLocalPathWithoutProjection(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	repo := server.RegisterRepoAccess("repo-id", "svn+ssh://_filees-client@example/repo", "/wc/repo", "office", contract.AccessReadWrite)
	repo.SetProjectedMetadata("Docs", "svn+ssh://_filees-client@example/repo", contract.AccessReadWrite, "active", "realm", "optional", true)
	server.MarkRepoDetached("office", "repo-id")
	summary := repo.Summary()
	if summary.Attached || summary.LocalPath != "" || summary.State != contract.StateUnattached {
		t.Fatalf("detached summary=%+v", summary)
	}
}

func TestProjectedPolicyReachesSummaryAndSnapshot(t *testing.T) {
	owner := "7b807185-aa75-4169-8a65-705c7cbab176"
	rs := New(t.TempDir()+"/daemon.sock").RegisterProjectedRepoPolicy("repo-id", "Team", "svn+ssh://_filees-client@example/repo", "office", contract.AccessReadWrite, "active", owner, "required", false)
	summary, snapshot := rs.Summary(), rs.Snapshot()
	if summary.OwnerRealmID != owner || summary.AttachmentPolicy != "required" {
		t.Fatalf("summary=%+v", summary)
	}
	if snapshot.OwnerRealmID != owner || snapshot.AttachmentPolicy != "required" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
