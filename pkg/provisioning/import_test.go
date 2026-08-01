package provisioning

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"filees/pkg/client"
	control "filees/pkg/control/v1"
)

func TestScanInitialSnapshotIsDeterministicAndExcludesMetadata(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"z", "a/nested", ".svn/tmp", ".filees/state", "a/node_modules", ".git"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{"z/last": "zz", "a/first": "a", "a/nested/middle": "mid", ".svn/tmp/ignored": "x", ".filees/state/ignored": "x", "a/old.bak": "x", "a/node_modules/package.json": "x", ".git/config": "x"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := ScanInitialSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "z", filepath.Join("a", "nested")}; !reflect.DeepEqual(snapshot.Directories, want) {
		t.Fatalf("directories = %#v, want %#v", snapshot.Directories, want)
	}
	if got, want := filePaths(snapshot.Files), []string{filepath.Join("a", "first"), filepath.Join("a", "nested", "middle"), filepath.Join("z", "last")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
	if snapshot.TotalBytes != 6 {
		t.Fatalf("total bytes = %d", snapshot.TotalBytes)
	}
}

func TestScanInitialSnapshotRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ScanInitialSnapshot(root); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestPublishInitialSnapshotBatchesAndResumesAfterFailure(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := readyOperation(t, root)
	opID := onlyOperationID(t, store)
	svn := &fakeInitialSVN{root: root, items: map[string]string{}, revision: 0, failCommit: 2}
	limits := ImportLimits{MaxBatchFiles: 2, MaxBatchBytes: 2}
	if _, err := PublishInitialSnapshot(context.Background(), store, opID, uuid.NewString(), svn, limits); err == nil {
		t.Fatal("expected injected second-commit failure")
	}
	if svn.revision != 1 || len(svn.commits) != 1 || !reflect.DeepEqual(svn.commits[0], []string{"a", "b"}) {
		t.Fatalf("partial publication: revision=%d commits=%#v", svn.revision, svn.commits)
	}

	svn.failCommit = 0
	op, err := PublishInitialSnapshot(context.Background(), store, opID, uuid.NewString(), svn, limits)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateInitialSnapshotPublished || op.Revision != 2 || op.Paths != 3 {
		t.Fatalf("published operation = %#v", op)
	}
	if len(svn.commits) != 2 || !reflect.DeepEqual(svn.commits[1], []string{"c"}) {
		t.Fatalf("resume recommitted prior batch: %#v", svn.commits)
	}
}

func TestPublishEmptyInitialSnapshotAcknowledgesRevisionZero(t *testing.T) {
	root := t.TempDir()
	store := readyOperation(t, root)
	opID := onlyOperationID(t, store)
	svn := &fakeInitialSVN{root: root, items: map[string]string{}}
	op, err := PublishInitialSnapshot(context.Background(), store, opID, uuid.NewString(), svn, ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if op.Revision != 0 || op.Paths != 0 || len(svn.commits) != 0 {
		t.Fatalf("empty publication = %#v, commits=%#v", op, svn.commits)
	}
}

func TestPublishCommitsFileLargerThanBatchLimitAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := readyOperation(t, root)
	svn := &fakeInitialSVN{root: root, items: map[string]string{}}
	op, err := PublishInitialSnapshot(context.Background(), store, onlyOperationID(t, store), uuid.NewString(), svn, ImportLimits{MaxBatchFiles: 1, MaxBatchBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if op.Revision != 1 || len(svn.commits) != 1 || len(svn.commits[0]) != 1 || svn.commits[0][0] != "large" {
		t.Fatalf("large-file publication = %#v, commits=%#v", op, svn.commits)
	}
}

func TestPublishRecoversMissingScheduledAddAfterFailedCommit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := readyOperation(t, root)
	svn := &fakeInitialSVN{root: root, items: map[string]string{}, extraStatus: []client.StatusEntry{{Path: "removed.bin", Item: "missing"}}}
	op, err := PublishInitialSnapshot(context.Background(), store, onlyOperationID(t, store), uuid.NewString(), svn, ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if op.Revision != 1 || len(svn.extraStatus) != 0 || len(svn.commits) != 1 || len(svn.commits[0]) != 1 || svn.commits[0][0] != "kept.txt" {
		t.Fatalf("recovered publication = %#v, status=%#v commits=%#v", op, svn.extraStatus, svn.commits)
	}
}

func readyOperation(t *testing.T, localPath string) *Store {
	t.Helper()
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID, reqID := uuid.NewString(), uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", localPath, "Repo")
	_, _ = store.RequestRepository(opID, reqID)
	_, err := store.ApplyRepositoryResult(result(t, opID, reqID, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: "repo", RepoURL: "file:///repo"}))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func onlyOperationID(t *testing.T, store *Store) string {
	t.Helper()
	ops, err := store.List()
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations = %#v, %v", ops, err)
	}
	return ops[0].OperationID
}

type fakeInitialSVN struct {
	root        string
	items       map[string]string
	revision    int64
	commitTry   int
	failCommit  int
	commits     [][]string
	extraStatus []client.StatusEntry
}

func (f *fakeInitialSVN) Checkout(context.Context, string, string) (string, error) {
	return "", os.MkdirAll(filepath.Join(f.root, ".svn"), 0o700)
}

func (f *fakeInitialSVN) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	snapshot, err := ScanInitialSnapshot(f.root)
	if err != nil {
		return nil, err
	}
	paths := append(append([]string{}, snapshot.Directories...), filePaths(snapshot.Files)...)
	entries := make([]client.StatusEntry, 0, len(paths))
	for _, path := range paths {
		item := f.items[path]
		if item == "" {
			item = "unversioned"
		}
		entries = append(entries, client.StatusEntry{Path: path, Item: item})
	}
	entries = append(entries, f.extraStatus...)
	return entries, nil
}

func (f *fakeInitialSVN) Revert(_ context.Context, _ string, paths []string) (string, error) {
	remove := make(map[string]bool, len(paths))
	for _, path := range paths {
		remove[filepath.Clean(path)] = true
		delete(f.items, filepath.Clean(path))
	}
	kept := f.extraStatus[:0]
	for _, entry := range f.extraStatus {
		if !remove[filepath.Clean(entry.Path)] {
			kept = append(kept, entry)
		}
	}
	f.extraStatus = kept
	return "", nil
}

func (f *fakeInitialSVN) Add(_ context.Context, _ string, paths []string) (string, error) {
	for _, path := range paths {
		f.items[path] = "added"
	}
	return "", nil
}

func (f *fakeInitialSVN) Commit(_ context.Context, _ string, paths []string, _ string) (string, error) {
	f.commitTry++
	if f.failCommit == f.commitTry {
		return "", errors.New("injected commit failure")
	}
	copyPaths := append([]string{}, paths...)
	f.commits = append(f.commits, copyPaths)
	for _, path := range paths {
		f.items[path] = "normal"
	}
	f.revision++
	return "", nil
}

func (f *fakeInitialSVN) Revision(context.Context, string) (int64, error) { return f.revision, nil }
