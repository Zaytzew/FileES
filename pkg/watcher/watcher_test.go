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
	if err := os.Rename(wc, moved); err != nil {
		t.Fatal(err)
	}
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
