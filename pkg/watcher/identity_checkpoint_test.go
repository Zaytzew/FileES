package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityCheckpointDoesNotAcknowledgeChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	before := []diskEntry{
		{Path: "stable", Size: 1, Mtime: 10, MD5: "a"},
		{Path: "edited", Size: 1, Mtime: 10, MD5: "a"},
		{Path: "missing", Size: 1, Mtime: 10, MD5: "a"},
		{Path: "unknown-hash", Size: 1, Mtime: 10},
		{Path: "known", Size: 1, Mtime: 10, MD5: "a", Identity: "original"},
	}
	b, _ := json.Marshal(before)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{statePath: path, requireRenameIdentity: true}
	observed := index{
		"stable":       {Size: 1, MtimeSec: 10, MD5: "a", Identity: "stable-id"},
		"edited":       {Size: 2, MtimeSec: 11, MD5: "b", Identity: "edited-id"},
		"unknown-hash": {Size: 1, MtimeSec: 10, Identity: "unproven"},
		"known":        {Size: 1, MtimeSec: 10, MD5: "a", Identity: "replacement"},
		"new":          {Size: 1, MtimeSec: 10, MD5: "a", Identity: "new-id"},
	}
	if err := s.checkpointIdentities(observed); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var after []diskEntry
	if err := json.Unmarshal(b, &after); err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("acknowledged paths")
	}
	for i, e := range after {
		want := before[i]
		if want.Path == "stable" {
			want.Identity = "stable-id"
		}
		if e != want {
			t.Fatalf("entry=%+v want=%+v", e, want)
		}
	}
}

func TestIdentityMigrationSurvivesCrashBeforeReplacement(t *testing.T) {
	wc := t.TempDir()
	old := filepath.Join(wc, "old.txt")
	if err := os.WriteFile(old, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if fileIdentity(old) == "" {
		t.Skip("stable file identity unavailable")
	}
	legacy := publicationScanner(t, wc)
	publicationScan(t, legacy)
	native := publicationScanner(t, wc)
	native.requireRenameIdentity = true
	publicationScan(t, native)
	// No orderly shutdown and no publication acknowledgement: only the
	// identity enrichment may survive this simulated process loss.
	newPath := filepath.Join(wc, "new V2.txt")
	if err := os.WriteFile(newPath, []byte("new version"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	restarted := publicationScanner(t, wc)
	restarted.requireRenameIdentity = true
	events := publicationScan(t, restarted)
	if len(events) != 1 || events[0].Op != Added || events[0].Rel != "new V2.txt" {
		t.Fatalf("replacement misclassified after crash: %+v", events)
	}
	// The new uncommitted file must remain discoverable after another crash.
	again := publicationScanner(t, wc)
	again.requireRenameIdentity = true
	events = publicationScan(t, again)
	if len(events) != 1 || events[0].Op != Added {
		t.Fatalf("unpublished addition swallowed: %+v", events)
	}
}
