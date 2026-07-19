package ipcserver

import (
	"context"
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
