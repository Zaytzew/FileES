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
