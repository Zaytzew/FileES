package localrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteDeletionPersistsWithoutTouchingWorkingCopy(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "wc")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"modified.txt": "unsent work", ".svn/wc.db": "metadata", "untracked.txt": "private draft"}
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(wc, p), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := store.EnsureConfiguredAttached("lab", "11111111-1111-4111-8111-111111111111", "svn+ssh://host/repo", "rw", wc, "Docs")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveRemoteDeletion(rec.ServerID, rec.RepoID); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := store.ObserveRemoteDeletion(rec.ServerID, rec.RepoID); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("replay changed receipt")
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, created, err := store.EnsureConfiguredAttached(rec.ServerID, rec.RepoID, rec.RepoURL, rec.Access, wc, "Docs")
	if err != nil || created || restored.State != StateDetached || !restored.RemoteDeletionObserved || restored.LocalCleanupCompleted {
		t.Fatalf("restart: %+v %v", restored, err)
	}
	if _, err := store.MarkAttached(rec.OperationID, rec.RepoID); err == nil {
		t.Fatal("late completion resurrected repo")
	}
	if _, err := store.BeginAttach(rec.ServerID, rec.RepoID, filepath.Join(root, "clone"), false); err == nil {
		t.Fatal("reattached terminal UUID")
	}
	if _, err := store.BeginAttach(rec.ServerID, "22222222-2222-4222-8222-222222222222", filepath.Join(root, "new-docs"), false); err != nil {
		t.Fatal("different UUID blocked", err)
	}
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(wc, p))
		if err != nil || string(got) != want {
			t.Fatalf("changed local %s", p)
		}
	}
}
