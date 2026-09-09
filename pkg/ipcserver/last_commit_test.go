package ipcserver

import (
	contract "filees/pkg/contract/v1"
	"os"
	"path/filepath"
	"testing"
)

func TestLastCommitSnapshotRequiresMatchingRevision(t *testing.T) {
	wc := t.TempDir()
	dir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("head.rev", "7")
	rs := New(t.TempDir()+"/daemon.sock").RegisterRepoAccess("r", "svn://host/r", wc, "s", contract.AccessReadWrite)
	write("last_commit.json", `{"revision":7,"date":"2026-07-01T10:00:00Z"}`)
	if got := rs.Snapshot().LastCommitAt; got != "2026-07-01T10:00:00Z" {
		t.Fatalf("date=%q", got)
	}
	write("head.rev", "8")
	if rs.Snapshot().LastCommitAt != "" {
		t.Fatal("stale revision date accepted")
	}
	write("last_commit.json", `{"revision":8,"date":"oops"}`)
	if rs.Snapshot().LastCommitAt != "" {
		t.Fatal("invalid date accepted")
	}
	write("last_commit.json", `{`)
	if rs.Snapshot().LastCommitAt != "" {
		t.Fatal("partial cache accepted")
	}
}
