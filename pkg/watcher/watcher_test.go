package watcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/filepolicy"
)

func TestMD5BacklogHashHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	if err := os.WriteFile(path, make([]byte, 1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := md5FileBudgetedContext(ctx, path, 1024*1024+1); !errors.Is(err, context.Canceled) {
		t.Fatalf("hash cancellation error = %v", err)
	}
}

func TestBuiltinIgnoresLibreOfficeLockMarker(t *testing.T) {
	cases := []struct {
		rel    string
		ignore bool
	}{
		{".~lock.report.doc#", true},
		{".~lock.powykonawczy12_9-2006.docx#", true},
		{"sub/dir/.~lock.spreadsheet.xlsx#", true},
		{"report.doc", false},
		{"~$report.doc", true}, // pre-existing MS Office pattern, sanity check
	}
	for _, c := range cases {
		got := false
		for _, pattern := range filepolicy.BuiltinIgnorePatterns {
			if (glob{raw: pattern}).match(c.rel, false) {
				got = true
				break
			}
		}
		if got != c.ignore {
			t.Errorf("isIgnored(%q) = %v, want %v", c.rel, got, c.ignore)
		}
	}
}

func TestScannerClosesEventsAfterWorkersStop(t *testing.T) {
	wc := t.TempDir()
	scanner, err := NewScanner(Options{
		WC: wc, StatePath: filepath.Join(wc, ".filees", "state", "manifest.json"),
		ScanPeriod: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := scanner.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner did not close events after cancellation")
	}
}

func TestWorkingCopySizeComesFromBufferedManifest(t *testing.T) {
	wc := t.TempDir()
	stateDir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(stateDir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`[{"path":"one.bin","mtime":1,"size":12},{"path":"dir/","mtime":1},{"path":"dir/two.bin","mtime":1,"size":30}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner, err := NewScanner(Options{WC: wc, StatePath: manifest, ScanPeriod: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if size, known := scanner.WorkingCopySize(); !known || size != 42 {
		t.Fatalf("buffered working-copy size = %d known=%v", size, known)
	}
}

// renameLiveDir moves a directory that a running scanner is walking.
//
// POSIX renames a directory regardless of who holds it open, so this was a
// bare os.Rename and passed everywhere. Windows refuses with "Access is
// denied" while any handle inside the tree is open, and the scanner here runs
// on a 5 ms period - so in a loaded parallel run the walk is often in flight
// exactly then, and the test failed on its own setup rather than on what it
// asserts. It measures whether the scanner RECREATES an abandoned root after
// the move; the move itself is only how it gets there.
//
// The refusal is transient - the walk holds the handle for a fraction of each
// period - so retrying finds a gap, which is also what a person moving the
// folder in Explorer does. Skipping the test on Windows would drop coverage of
// a case that matters most on the platform where users drag folders around.
func renameLiveDir(t *testing.T, from, to string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := os.Rename(from, to)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rename %s: still refused after 5s: %v", from, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestScannerDoesNotRecreateMovedWorkingCopy(t *testing.T) {
	parent := t.TempDir()
	wc := filepath.Join(parent, "documents")
	stateDir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(stateDir, "manifest.json")
	scanner, err := NewScanner(Options{WC: wc, StatePath: manifest, ScanPeriod: 5 * time.Millisecond, RequireSVNMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := scanner.Start(ctx)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(manifest); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scanner did not create its initial manifest")
		}
		time.Sleep(time.Millisecond)
	}
	moved := filepath.Join(parent, "documents-moved")
	renameLiveDir(t, wc, moved)
	cancel()
	for range events {
	}
	if _, err := os.Stat(wc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scanner recreated abandoned working-copy root: %v", err)
	}
}

// TestAtomicWriteJSONDoesNotFollowPredictableSymlink is the watcher half of the
// audit's Finding D regression coverage; see pkg/commit's equivalent for the
// full attack description.
func TestAtomicWriteJSONDoesNotFollowPredictableSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	const original = "precious user data"
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "manifest.json")
	if err := os.Symlink(victim, state+".tmp"); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	if err := atomicWriteJSON(state, []string{"daemon-content"}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("atomicWriteJSON followed the symlink and overwrote the target: %q", got)
	}
	info, err := os.Lstat(state)
	if err != nil {
		t.Fatalf("atomicWriteJSON did not write the state file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("atomicWriteJSON left a symlink at the state path")
	}
}
