//go:build !windows

package privatefile

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestVerifyUsesEffectiveFilesystemIdentity(t *testing.T) {
	path := t.TempDir()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("temporary directory exposes no Unix ownership")
	}

	original := effectiveUID
	t.Cleanup(func() { effectiveUID = original })
	effectiveUID = func() int { return int(stat.Uid) }
	if err := Verify(path); err != nil {
		t.Fatalf("effective owner rejected: %v", err)
	}

	effectiveUID = func() int { return int(stat.Uid) + 1 }
	if err := Verify(path); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("foreign effective owner error = %v", err)
	}
}

// widen exposes the path to everyone, so the shared tests can prove Verify
// actually rejects it.
func widen(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o644)
	if info.IsDir() {
		mode = 0o755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
