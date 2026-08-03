//go:build !windows

package privatefile

import (
	"os"
	"testing"
)

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
