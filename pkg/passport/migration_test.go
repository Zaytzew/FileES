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
	appendOnly          map[string]bool
	sets, dels, commits [][]string
}

func (c *migrationClient) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	return c.status, nil
}
func (c *migrationClient) PropList(_ context.Context, _ string, propName string) (map[string]bool, error) {
	if propName == AppendOnlyProperty {
		return c.appendOnly, nil
	}
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
	if _, err := EnsureNeedsLock(context.Background(), c, wc, "instance", 1); err != nil {
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
	if _, err := EnsureNeedsLock(context.Background(), c, wc, "instance", 100); !errors.Is(err, ErrWorkingCopyDirty) {
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
	if _, err := ClearNeedsLock(context.Background(), c, wc, "instance", 1); err != nil {
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
	if _, err := ClearNeedsLock(context.Background(), c, wc, "instance", 100); !errors.Is(err, ErrWorkingCopyDirty) {
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
	if _, err := ClearNeedsLock(context.Background(), c, wc, "instance", 100); err != nil {
		t.Fatal(err)
	}
	if len(c.dels) != 0 || len(c.commits) != 0 {
		t.Fatalf("nothing carried the property yet it committed: dels=%#v commits=%#v", c.dels, c.commits)
	}
}

// The mobile channel is append-only and takes no part in edit passports, so
// the svn:needs-lock it sets states a different intent: those files must not
// be edited at all, whatever the repository's policy. Turning the policy off
// must therefore leave them locked - the policy did not put that barrier
// there and has no business removing it.
func TestClearNeedsLockLeavesAppendOnlyUploadsLocked(t *testing.T) {
	wc := t.TempDir()
	c := &migrationClient{
		props:      map[string]bool{"drawing.dwg": true, "photos/IMG_1.jpg": true},
		appendOnly: map[string]bool{"photos/IMG_1.jpg": true},
		status: []client.StatusEntry{
			{Path: "drawing.dwg", Item: "normal"},
			{Path: "photos/IMG_1.jpg", Item: "normal"},
		},
	}
	if _, err := ClearNeedsLock(context.Background(), c, wc, "instance", 100); err != nil {
		t.Fatal(err)
	}
	if len(c.dels) != 1 || len(c.dels[0]) != 1 || c.dels[0][0] != "drawing.dwg" {
		t.Fatalf("rollback touched append-only uploads: dels=%#v", c.dels)
	}
}

// A lock-aware client used to exercise the foreign-hold path.
type lockAwareMigrationClient struct {
	migrationClient
	locks []client.LockEntry
}

func (c *lockAwareMigrationClient) ListLocks(context.Context, string) ([]client.LockEntry, error) {
	return c.locks, nil
}

// SVN refuses an entire commit if any single path in it is locked by somebody
// else, so one colleague editing one file used to block the whole
// repository's migration. Measured live on 2026-08-11. The held path is now
// left alone and reported, and everything else still migrates.
func TestClearNeedsLockSkipsHeldPathsInsteadOfFailingTheWholeCommit(t *testing.T) {
	wc := t.TempDir()
	c := &lockAwareMigrationClient{
		migrationClient: migrationClient{
			props: map[string]bool{"free.dwg": true, "held.dwg": true},
			status: []client.StatusEntry{
				{Path: "free.dwg", Item: "normal"},
				{Path: "held.dwg", Item: "normal"},
			},
		},
		locks: []client.LockEntry{{Path: "held.dwg", LockInfo: client.LockInfo{Token: "tok-1", Owner: "someone-else"}}},
	}

	skipped, err := ClearNeedsLock(context.Background(), c, wc, "instance", 100)
	if err != nil {
		t.Fatalf("one held path failed the whole migration: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped=%d, want the single held path reported", skipped)
	}
	if len(c.dels) != 1 || len(c.dels[0]) != 1 || c.dels[0][0] != "free.dwg" {
		t.Fatalf("migration did not proceed for the unheld path: dels=%#v", c.dels)
	}
}

// The forward direction has the same failure mode and the same fix.
func TestEnsureNeedsLockSkipsHeldPaths(t *testing.T) {
	wc := t.TempDir()
	for _, p := range []string{"free.dwg", "held.dwg"} {
		if err := os.WriteFile(filepath.Join(wc, p), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	c := &lockAwareMigrationClient{
		migrationClient: migrationClient{
			props: map[string]bool{},
			status: []client.StatusEntry{
				{Path: "free.dwg", Item: "normal", Props: "none"},
				{Path: "held.dwg", Item: "normal", Props: "none"},
			},
		},
		locks: []client.LockEntry{{Path: "held.dwg", LockInfo: client.LockInfo{Token: "tok-1", Owner: "someone-else"}}},
	}

	skipped, err := EnsureNeedsLock(context.Background(), c, wc, "instance", 100)
	if err != nil {
		t.Fatalf("one held path failed the whole migration: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped=%d, want the single held path reported", skipped)
	}
	if len(c.sets) != 1 || len(c.sets[0]) != 1 || c.sets[0][0] != "free.dwg" {
		t.Fatalf("migration did not proceed for the unheld path: sets=%#v", c.sets)
	}
}

// A client that cannot enumerate locks keeps the old all-or-nothing
// behaviour rather than silently skipping everything it cannot check.
func TestMigrationWithoutLockListerStillMigratesEverything(t *testing.T) {
	wc := t.TempDir()
	c := &migrationClient{
		props:  map[string]bool{"a.dwg": true},
		status: []client.StatusEntry{{Path: "a.dwg", Item: "normal"}},
	}
	skipped, err := ClearNeedsLock(context.Background(), c, wc, "instance", 100)
	if err != nil || skipped != 0 {
		t.Fatalf("skipped=%d err=%v", skipped, err)
	}
	if len(c.dels) != 1 {
		t.Fatalf("nothing migrated without a lock lister: dels=%#v", c.dels)
	}
}
