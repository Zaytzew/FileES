package ipcserver

import (
	"testing"

	contract "filees/pkg/contract/v1"
)

// A running publication is part of the repository's status while it runs and
// gone from it when it ends; the recorded value is a copy, not the caller's.
func TestPublishProgressIsPartOfTheRepositoryStatus(t *testing.T) {
	rs := New(t.TempDir()+"/daemon.sock").RegisterRepoAccess("docs", "svn+ssh://host/docs", t.TempDir(), "office", contract.AccessReadWrite)
	progress := &contract.PublishProgress{FilesDone: 3, FilesTotal: 10, BytesSent: 300, BytesTotal: 1000, StartedAt: "2026-09-24T12:00:00Z"}
	rs.SetPublishProgress(progress)
	progress.FilesDone = 99
	got := rs.Snapshot().PublishProgress
	if got == nil || got.FilesDone != 3 || got.FilesTotal != 10 || got.BytesTotal != 1000 {
		t.Fatalf("status progress = %+v", got)
	}
	rs.SetPublishProgress(nil)
	if rs.Snapshot().PublishProgress != nil {
		t.Fatal("the finished publication is still reported")
	}
}
