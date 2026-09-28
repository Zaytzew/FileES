//go:build linux || freebsd || openbsd || netbsd

package trash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMoveFollowsTheFreedesktopTrash(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	for i := 0; i < 2; i++ { // the same name twice gets a second slot
		folder := filepath.Join(root, "work", "Oppo Reno")
		if err := os.MkdirAll(filepath.Join(folder, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "sub", "a.dwg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Move(folder); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			t.Fatalf("folder still present: %v", err)
		}
	}
	trash := filepath.Join(root, "data", "Trash")
	for _, name := range []string{"Oppo Reno", "Oppo Reno.1"} {
		if _, err := os.Stat(filepath.Join(trash, "files", name, "sub", "a.dwg")); err != nil {
			t.Fatalf("%s not in the trash: %v", name, err)
		}
		info, err := os.ReadFile(filepath.Join(trash, "info", name+".trashinfo"))
		if err != nil || !strings.Contains(string(info), "Path=") || !strings.Contains(string(info), "Oppo%20Reno") || !strings.Contains(string(info), "DeletionDate=") {
			t.Fatalf("trashinfo %s = %q, %v", name, info, err)
		}
	}
	if err := Move(filepath.Join(root, "work", "Oppo Reno")); err != nil {
		t.Fatalf("a folder already gone is done: %v", err)
	}
}
