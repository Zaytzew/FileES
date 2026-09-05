package main

import (
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
)

func TestRemoteCleanupRetriesWithoutLosingPreCleanupInspection(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "wc")
	outside := filepath.Join(root, "outside")
	for _, p := range []string{filepath.Join(wc, ".svn"), outside} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(wc, "drawing.txt"), filepath.Join(outside, "keep")} {
		if err := os.WriteFile(p, []byte("user work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(wc, ".filees")); err != nil {
		t.Skip(err)
	}
	path := filepath.Join(root, "lifecycle.json")
	store, err := localrepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := store.EnsureConfiguredAttached("lab", "11111111-1111-4111-8111-111111111111", "svn+ssh://lab/repo", "rw", wc, "Docs")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveRemoteDeletion(rec.ServerID, rec.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRemoteCleanup(rec.OperationID, "changed"); err != nil {
		t.Fatal(err)
	}
	key := reposupervisor.Key{ServerID: rec.ServerID, RepoID: rec.RepoID}
	if err := cleanupRemoteDeletedCopies(t.Context(), store, key); err == nil {
		t.Fatal("unsafe metadata accepted")
	}
	got, _ := store.Get(rec.OperationID)
	if got.LocalCleanupCompleted || got.LastError == "" {
		t.Fatalf("failure hidden: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(wc, ".svn")); err != nil {
		t.Fatal("partial removal before validation", err)
	}
	if err := os.Remove(filepath.Join(wc, ".filees")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after .svn was removed but before the receipt was saved.
	if err := os.Rename(filepath.Join(wc, ".svn"), filepath.Join(root, "removed-svn")); err != nil {
		t.Fatal(err)
	}
	store, err = localrepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupRemoteDeletedCopies(t.Context(), store, key); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(rec.OperationID)
	if !got.LocalCleanupCompleted || got.PreservedCopyStatus != "changed" || got.LastError != "" {
		t.Fatalf("restart lost evidence: %+v", got)
	}
	for _, p := range []string{filepath.Join(wc, "drawing.txt"), filepath.Join(outside, "keep")} {
		raw, err := os.ReadFile(p)
		if err != nil || string(raw) != "user work" {
			t.Fatalf("user data changed: %v", err)
		}
	}
}

func TestMetadataCleanupRefusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	link := filepath.Join(root, "link")
	if err := os.MkdirAll(filepath.Join(actual, "wc", ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Skip(err)
	}
	if err := stripWorkingCopyMetadata(filepath.Join(link, "wc"), "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("symlinked ancestor accepted")
	}
	if _, err := os.Stat(filepath.Join(actual, "wc", ".svn")); err != nil {
		t.Fatal("metadata removed through symlink")
	}
}
