package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/talk"
)

// A commit.busy marker outlives the process that wrote it whenever a commit is
// killed. Detecting it as stale is not enough: if the file stays, every later
// scan re-detects the same marker. One working copy produced this warning every
// ~15 seconds for nine days before anyone looked.
func TestStaleBusyMarkerIsRemovedNotJustIgnored(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "commit.busy")
	if err := os.WriteFile(marker, []byte("pid=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}

	scanner := &Scanner{busyPath: marker, busyTTL: time.Minute, lg: talk.With("test")}

	if scanner.isBusy() {
		t.Fatal("an expired marker must not report the working copy as busy")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stale marker still present after isBusy: %v", err)
	}
	// Second call must be quiet and stable rather than repeating forever.
	if scanner.isBusy() {
		t.Fatal("isBusy changed its answer once the marker was gone")
	}
}

// A marker written by a live commit must be respected, otherwise removing stale
// ones would race a running commit into a double run.
func TestFreshBusyMarkerIsKept(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "commit.busy")
	if err := os.WriteFile(marker, []byte("pid=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanner := &Scanner{busyPath: marker, busyTTL: time.Hour, lg: talk.With("test")}

	if !scanner.isBusy() {
		t.Fatal("a fresh marker must report busy")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a fresh marker must survive: %v", err)
	}
}
