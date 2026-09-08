package ipcserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadPendingStatsCountsFilesAndKnownBytes(t *testing.T) {
	root := t.TempDir()
	added := filepath.Join(root, "added.bin")
	modified := filepath.Join(root, "modified.bin")
	if err := os.WriteFile(added, make([]byte, 11), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modified, make([]byte, 7), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []cacheEntry{
		{Abs: added, Op: "added"},
		{Abs: modified, Op: "modified"},
		{Abs: filepath.Join(root, "gone.bin"), Op: "deleted"},
		{Abs: root, IsDir: true, Op: "added"},
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "cache.json")
	if err := os.WriteFile(cache, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got := readPendingStats(cache)
	if got.Added != 2 || got.Modified != 1 || got.Deleted != 1 || got.TotalBytes != 18 {
		t.Fatalf("pending stats = %+v", got)
	}
}

// The measured defect (NATIVE-PENDING-PROJECTION, live daemon 2026-09-08): a
// working copy held on an unconfirmed rename reported added/modified/deleted
// all zero over a non-zero total_bytes, because the byte sum ran for entries
// the switch never counted. The interface could not tell "nothing to do" from
// "stuck", and showed the first.
func TestReadPendingStatsCountsRenamesAndHeldRenames(t *testing.T) {
	root := t.TempDir()
	renamed := filepath.Join(root, "renamed.bin")
	uncertain := filepath.Join(root, "uncertain.bin")
	if err := os.WriteFile(renamed, make([]byte, 20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uncertain, make([]byte, 21), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]cacheEntry{
		{Abs: renamed, Op: "renamed"},
		{Abs: uncertain, Op: "rename_uncertain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "cache.json")
	if err := os.WriteFile(cache, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got := readPendingStats(cache)
	if got.Renamed != 1 || got.RenameUncertain != 1 {
		t.Fatalf("renames not counted: %+v", got)
	}
	if got.TotalBytes != 41 {
		t.Fatalf("total bytes = %d, want 41", got.TotalBytes)
	}
	// The shape of the bug, stated directly: bytes without a count.
	if got.Added+got.Modified+got.Deleted+got.Renamed+got.RenameUncertain == 0 && got.TotalBytes > 0 {
		t.Fatal("pending bytes with an empty queue is the projection lying again")
	}
}

// Held renames are counted apart from ordinary ones because they mean
// different things: a rename is queued, an unconfirmed rename is not going to
// publish until something changes. Folding them together would say "soon"
// about something stuck.
func TestReadPendingStatsSeparatesHeldRenames(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "held.bin")
	if err := os.WriteFile(path, make([]byte, 5), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]cacheEntry{{Abs: path, Op: "rename_uncertain"}})
	cache := filepath.Join(root, "cache.json")
	if err := os.WriteFile(cache, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got := readPendingStats(cache)
	if got.Renamed != 0 || got.RenameUncertain != 1 {
		t.Fatalf("held rename must not be counted as an ordinary one: %+v", got)
	}
}
