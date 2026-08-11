package passport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

type migrationClient struct {
	status              []client.StatusEntry
	props               map[string]bool
	sets, dels, commits [][]string
}

func (c *migrationClient) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	return c.status, nil
}
func (c *migrationClient) PropList(context.Context, string, string) (map[string]bool, error) {
	return c.props, nil
}
func (c *migrationClient) PropSet(_ context.Context, _ string, _, _ string, paths []string) (string, error) {
	c.sets = append(c.sets, append([]string(nil), paths...))
	return "", nil
}
func (c *migrationClient) PropDel(_ context.Context, _ string, _ string, paths []string) (string, error) {
	c.dels = append(c.dels, append([]string(nil), paths...))
	return "", nil
}
func (c *migrationClient) Commit(_ context.Context, _ string, paths []string, _ string) (string, error) {
	c.commits = append(c.commits, append([]string(nil), paths...))
	return "", nil
}

func TestEnsureNeedsLockMigratesRegularFilesInBatches(t *testing.T) {
	wc := t.TempDir()
	for _, p := range []string{"a.bin", "dir/b.bin"} {
		abs := filepath.Join(wc, p)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	c := &migrationClient{props: map[string]bool{}, status: []client.StatusEntry{{Path: "a.bin", Item: "normal", Props: "none"}, {Path: "dir/b.bin", Item: "normal", Props: "none"}, {Path: "new.bin", Item: "unversioned", Props: "none"}}}
	if err := EnsureNeedsLock(context.Background(), c, wc, "instance", 1); err != nil {
		t.Fatal(err)
	}
	if len(c.sets) != 2 || len(c.commits) != 2 {
		t.Fatalf("sets=%#v commits=%#v", c.sets, c.commits)
	}
}

func TestEnsureNeedsLockRefusesContentModification(t *testing.T) {
	wc := t.TempDir()
	path := filepath.Join(wc, "dirty.bin")
	if err := os.WriteFile(path, []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	c := &migrationClient{props: map[string]bool{}, status: []client.StatusEntry{{Path: "dirty.bin", Item: "modified"}}}
	if err := EnsureNeedsLock(context.Background(), c, wc, "instance", 100); !errors.Is(err, ErrWorkingCopyDirty) {
		t.Fatalf("error=%v", err)
	}
	if len(c.commits) != 0 {
		t.Fatal("dirty content was committed")
	}
}

// Without a working rollback the policy is a one-way door: svn:needs-lock is
// versioned, so it outlives the policy and leaves every client staring at
// read-only files with no machinery left to unlock them.
func TestClearNeedsLockRemovesThePropertyInBatchesAndCommits(t *testing.T) {
	wc := t.TempDir()
	c := &migrationClient{
		props: map[string]bool{"a.bin": true, "dir/b.bin": true},
		status: []client.StatusEntry{
			{Path: "a.bin", Item: "normal"},
			{Path: "dir/b.bin", Item: "normal"},
			{Path: "untouched.bin", Item: "normal"},
			{Path: "new.bin", Item: "unversioned"},
		},
	}
	if err := ClearNeedsLock(context.Background(), c, wc, "instance", 1); err != nil {
		t.Fatal(err)
	}
	if len(c.dels) != 2 || len(c.commits) != 2 {
		t.Fatalf("dels=%#v commits=%#v", c.dels, c.commits)
	}
	// Only paths that actually carry the property are touched, so the commit
	// stays proportional to the change instead of rewriting the whole tree.
	for _, batch := range c.dels {
		for _, p := range batch {
			if p == "untouched.bin" || p == "new.bin" {
				t.Fatalf("propdel reached a path without the property: %#v", c.dels)
			}
		}
	}
}

// Same refusal as the forward migration: a property-only commit must never
// carry user content that was edited before the policy changed.
func TestClearNeedsLockRefusesDirtyWorkingCopy(t *testing.T) {
	wc := t.TempDir()
	c := &migrationClient{
		props:  map[string]bool{"dirty.bin": true},
		status: []client.StatusEntry{{Path: "dirty.bin", Item: "modified"}},
	}
	if err := ClearNeedsLock(context.Background(), c, wc, "instance", 100); !errors.Is(err, ErrWorkingCopyDirty) {
		t.Fatalf("error=%v", err)
	}
	if len(c.commits) != 0 || len(c.dels) != 0 {
		t.Fatalf("dirty working copy was modified: dels=%#v commits=%#v", c.dels, c.commits)
	}
}

// Resuming after an interrupted rollback must not re-delete what is already
// gone, so a half-finished run simply continues.
func TestClearNeedsLockIsIdempotent(t *testing.T) {
	wc := t.TempDir()
	c := &migrationClient{
		props:  map[string]bool{},
		status: []client.StatusEntry{{Path: "a.bin", Item: "normal"}, {Path: "dir/b.bin", Item: "normal"}},
	}
	if err := ClearNeedsLock(context.Background(), c, wc, "instance", 100); err != nil {
		t.Fatal(err)
	}
	if len(c.dels) != 0 || len(c.commits) != 0 {
		t.Fatalf("nothing carried the property yet it committed: dels=%#v commits=%#v", c.dels, c.commits)
	}
}
