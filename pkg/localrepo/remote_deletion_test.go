package localrepo

import (
	"errors"
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

func TestRemoteCleanupReceiptsSurviveRestartAndKeepAuthorityFence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.EnsureConfiguredAttached("lab", "11111111-1111-4111-8111-111111111111", "svn+ssh://lab/repo", "rw", filepath.Join(root, "wc"), "Docs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRemoteCleanup(r.OperationID); err == nil {
		t.Fatal("cleanup without authority accepted")
	}
	if err := s.ObserveRemoteDeletion(r.ServerID, r.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRemoteCleanup(r.OperationID); err == nil {
		t.Fatal("cleanup without inspection accepted")
	}
	if err := s.BeginRemoteCleanup(r.OperationID, "changed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAttach("lab", "22222222-2222-4222-8222-222222222222", r.LocalPath, false); err == nil {
		t.Fatal("reused path before cleanup")
	}
	if err := s.RecordPreservedCopyStatus(r.OperationID, "unknown"); err == nil {
		t.Fatal("pre-cleanup evidence overwritten")
	}
	if err := s.RecordRemoteCleanupError(r.OperationID, errors.New("locked")); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRemoteCleanup(r.OperationID); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.OperationID)
	if !got.LocalCleanupCompleted || !got.RemoteCleanupStarted || got.State != StateDetached || got.PreservedCopyStatus != "changed" || got.LastError != "" {
		t.Fatal(got)
	}
	if _, err := s.MarkAttached(r.OperationID, r.RepoID); err == nil {
		t.Fatal("cleanup reopened authority")
	}
	before, _ := os.ReadFile(path)
	if err := s.CompleteRemoteCleanup(r.OperationID); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("replayed cleanup rewrote state")
	}
	if _, err := s.BeginAttach("lab", "22222222-2222-4222-8222-222222222222", r.LocalPath, false); err != nil {
		t.Fatal("completed cleanup did not release path", err)
	}
}
