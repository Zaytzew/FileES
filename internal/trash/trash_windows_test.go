//go:build windows

package trash

import (
	"os"
	"path/filepath"
	"testing"
)

// Opt-in: it puts a small folder into the real Recycle Bin of whoever runs it.
func TestMoveSendsAFolderToTheRecycleBin(t *testing.T) {
	if os.Getenv("FILEES_TRASH_WINDOWS_TEST") != "1" {
		t.Skip("set FILEES_TRASH_WINDOWS_TEST=1 to use the real Recycle Bin")
	}
	folder := filepath.Join(t.TempDir(), "filees-trash-test")
	if err := os.MkdirAll(filepath.Join(folder, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "sub", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Move(folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("folder still present: %v", err)
	}
}
