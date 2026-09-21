package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/client"
)

func TestSyncPlanPublishesFilesAndStopsOnConflict(t *testing.T) {
	add, commit, err := syncPlan([]client.StatusEntry{
		{Path: "2026/09/a.pdf", Item: "unversioned"},
		{Path: "2026/09/b.pdf", Item: "modified"},
		{Path: ".", Item: "normal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(add) != 1 || add[0] != "2026/09/a.pdf" {
		t.Fatalf("add = %#v", add)
	}
	if len(commit) != 2 {
		t.Fatalf("commit = %#v", commit)
	}
	_, _, err = syncPlan([]client.StatusEntry{{Path: "2026/09/c.pdf", Item: "conflicted"}})
	if err == nil || !strings.Contains(err.Error(), "conflicted") {
		t.Fatalf("conflict err = %v", err)
	}
	_, _, err = syncPlan([]client.StatusEntry{{Path: "gone.pdf", Item: "missing"}})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing err = %v", err)
	}
}

func TestExpandUnversionedDirectoryListsFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.pdf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := expandUnversioned(root, []string{"2026"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "2026/09/a.pdf" {
		t.Fatalf("files = %#v", got)
	}
}

type fakeWC struct {
	updated bool
	added   []string
	message string
	entries []client.StatusEntry
}

func (f *fakeWC) Update(context.Context, string) (string, error) {
	f.updated = true
	return "At revision 3.\n", nil
}
func (f *fakeWC) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	return f.entries, nil
}
func (f *fakeWC) Add(_ context.Context, _ string, paths []string) (string, error) {
	f.added = append(f.added, paths...)
	return "", nil
}
func (f *fakeWC) Commit(_ context.Context, _ string, paths []string, message string) (string, error) {
	f.message = message
	return "Committed revision 4.\n", nil
}

func TestSyncWorkingCopyExpandsDirectoryBeforeCommit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.pdf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeWC{entries: []client.StatusEntry{{Path: "2026", Item: "unversioned"}}}
	if err := syncWorkingCopy(context.Background(), fake, root, "ksefpuck"); err != nil {
		t.Fatal(err)
	}
	if len(fake.added) != 1 || fake.added[0] != "2026/09/a.pdf" {
		t.Fatalf("added = %#v", fake.added)
	}
}

func TestSyncWorkingCopyUpdatesThenCommits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.pdf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeWC{entries: []client.StatusEntry{{Path: "a.pdf", Item: "unversioned"}}}
	if err := syncWorkingCopy(context.Background(), fake, root, "ksefpuck"); err != nil {
		t.Fatal(err)
	}
	if !fake.updated || len(fake.added) != 1 || fake.message != "ksefpuck" {
		t.Fatalf("fake = %+v", fake)
	}
}
