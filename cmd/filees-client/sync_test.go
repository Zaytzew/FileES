package main

import (
	"context"
	"fmt"
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

func TestSyncWorkingCopyStagesMissingBeforeUpdate(t *testing.T) {
	root := t.TempDir()
	fake := &fakeWC{statuses: [][]client.StatusEntry{
		{{Path: "gone.pdf", Item: "missing"}},
		{{Path: "gone.pdf", Item: "deleted"}},
	}}
	if err := syncWorkingCopy(context.Background(), fake, root, "publish"); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "gone.pdf" || !fake.updated {
		t.Fatalf("fake = %+v", fake)
	}
}

func TestSyncWorkingCopyRefusesPropertyConflictBeforeUpdate(t *testing.T) {
	fake := &fakeWC{entries: []client.StatusEntry{{Path: "a.pdf", Item: "modified", Props: "conflicted"}}}
	if err := syncWorkingCopy(context.Background(), fake, t.TempDir(), "publish"); err == nil || fake.updated {
		t.Fatalf("err = %v, fake = %+v", err, fake)
	}
}

func TestSyncPlanCommitsPropertyOnlyChangeAndDedupesMissingTree(t *testing.T) {
	_, commit, err := syncPlan([]client.StatusEntry{{Path: "a.pdf", Item: "normal", Props: "modified"}})
	if err != nil || len(commit) != 1 || commit[0] != "a.pdf" {
		t.Fatalf("commit=%v err=%v", commit, err)
	}
	got := topLevelPaths([]string{"folder/a.pdf", "folder", "other.pdf"})
	if len(got) != 2 || got[0] != "folder" || got[1] != "other.pdf" {
		t.Fatalf("top-level=%v", got)
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
	updated     bool
	added       []string
	deleted     []string
	message     string
	entries     []client.StatusEntry
	statuses    [][]client.StatusEntry
	statusCalls int
}

func (f *fakeWC) Update(context.Context, string) (string, error) {
	f.updated = true
	return "At revision 3.\n", nil
}
func (f *fakeWC) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	if f.statusCalls < len(f.statuses) {
		result := f.statuses[f.statusCalls]
		f.statusCalls++
		return result, nil
	}
	return f.entries, nil
}
func (f *fakeWC) Add(_ context.Context, _ string, paths []string) (string, error) {
	f.added = append(f.added, paths...)
	return "", nil
}
func (f *fakeWC) Delete(_ context.Context, _ string, paths []string) (string, error) {
	if f.updated {
		return "", fmt.Errorf("deletion staged after update")
	}
	f.deleted = append(f.deleted, paths...)
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
