package provisioning

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// The first publication of a new repository runs before the daemon knows the
// repository, so it reports its own progress: totals from the scanned
// snapshot, a zero before sending and each batch as it lands (the overlay
// showed only a spinner over a 227 MB import, 2026-09-24).
func TestPublishInitialSnapshotReportsProgressBatchByBatch(t *testing.T) {
	root := t.TempDir()
	for name, size := range map[string]int{"a": 3, "b": 5, "c": 7} {
		if err := os.WriteFile(filepath.Join(root, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := readyOperation(t, root)
	svn := &fakeInitialSVN{root: root, items: map[string]string{}}
	var reports []ImportProgress
	_, err := PublishInitialSnapshotWithProgress(context.Background(), store, onlyOperationID(t, store), uuid.NewString(), svn,
		ImportLimits{MaxBatchFiles: 2, MaxBatchBytes: 1024}, func(progress ImportProgress) { reports = append(reports, progress) })
	if err != nil {
		t.Fatal(err)
	}
	want := []ImportProgress{
		{FilesDone: 0, FilesTotal: 3, BytesSent: 0, BytesTotal: 15},
		{FilesDone: 2, FilesTotal: 3, BytesSent: 8, BytesTotal: 15},
		{FilesDone: 3, FilesTotal: 3, BytesSent: 15, BytesTotal: 15},
	}
	if !reflect.DeepEqual(reports, want) {
		t.Fatalf("reports = %+v, want %+v", reports, want)
	}
}

// A resumed import counts only what this run still has to send: files that
// reached the repository before the interruption are not in its totals.
func TestResumedInitialPublicationReportsOnlyWhatRemains(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("xx"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := readyOperation(t, root)
	opID := onlyOperationID(t, store)
	svn := &fakeInitialSVN{root: root, items: map[string]string{}, failCommit: 2}
	limits := ImportLimits{MaxBatchFiles: 2, MaxBatchBytes: 1024}
	if _, err := PublishInitialSnapshot(context.Background(), store, opID, uuid.NewString(), svn, limits); err == nil {
		t.Fatal("expected injected second-commit failure")
	}
	svn.failCommit = 0
	var last ImportProgress
	var first *ImportProgress
	if _, err := PublishInitialSnapshotWithProgress(context.Background(), store, opID, uuid.NewString(), svn, limits, func(progress ImportProgress) {
		if first == nil {
			copy := progress
			first = &copy
		}
		last = progress
	}); err != nil {
		t.Fatal(err)
	}
	if first == nil || first.FilesTotal != 1 || first.BytesTotal != 2 || last != (ImportProgress{FilesDone: 1, FilesTotal: 1, BytesSent: 2, BytesTotal: 2}) {
		t.Fatalf("first=%+v last=%+v", first, last)
	}
}
