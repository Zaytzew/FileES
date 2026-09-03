package linkservice

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The three ways a config can be unusable are three different repairs, and
// naming the wrong one costs more than saying nothing. Measured on cloud,
// 2026-09-03: filees-links could not traverse /etc/filees, whose group had been
// changed from _filees-public to wheel, and the service reported that the file
// was writable by group or others. Its mode was 0600. Following the message led
// to relaxing permissions that were already correct while the directory - the
// actual cause - went unexamined.
func TestEachWayTheConfigFailsSaysWhichOneItWas(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission semantics")
	}
	dir := t.TempDir()

	missing := filepath.Join(dir, "absent.json")
	if _, err := Load(missing); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("a config nobody can stat must say so: %v", err)
	}

	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(filepath.Join(dir, "target.json"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("a symlink must be named as one: %v", err)
	}

	loose := filepath.Join(dir, "loose.json")
	if err := os.WriteFile(loose, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Explicit, because umask strips the write bits WriteFile is asked for and
	// the file would arrive at 0644 - which passes, testing nothing.
	if err := os.Chmod(loose, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(loose); err == nil || !strings.Contains(err.Error(), "0666") {
		t.Fatalf("the offending mode is the one fact that makes this fixable: %v", err)
	}
}
