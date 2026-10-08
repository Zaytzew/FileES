//go:build windows && !nocfapi

package anchorrepair

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSystemReadsAnOrdinaryFolderAndRefusesToDeleteFromIt(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "sub", "plain.txt")
	if err := os.WriteFile(file, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	system := Windows{}
	if provider, err := system.SyncRootProvider(root); err == nil {
		t.Fatalf("an ordinary folder has provider %q", provider)
	}
	entries, err := system.List(root)
	if err != nil || len(entries) != 1 || entries[0].Name != "sub" || !entries[0].Directory || entries[0].Reparse || entries[0].Cloud {
		t.Fatalf("list: %+v %v", entries, err)
	}
	if unreadable, err := system.Unreadable(file); err != nil || unreadable {
		t.Fatalf("ordinary file: unreadable=%v err=%v", unreadable, err)
	}
	damaged, err := Scan(system, root)
	if err != nil || len(damaged) != 0 {
		t.Fatalf("scan: %v %v", damaged, err)
	}
	// Never an ordinary file, whatever the caller claims about it.
	if err := system.RemoveCloudEntry(root, file, false); err == nil {
		t.Fatal("an ordinary file was accepted for removal")
	}
	if err := system.RemoveCloudEntry(filepath.Join(root, "sub"), filepath.Join(root, "other.txt"), false); err == nil {
		t.Fatal("an entry outside the folder was accepted for removal")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("ordinary file is gone: %v", err)
	}
	if result := Repair(system, root, ""); result.Error != "not_anchor" || result.Detached {
		t.Fatalf("repair of an ordinary folder: %+v", result)
	}
}

// Read-only look at a real folder: FILEES_ANCHOR_REPAIR_SCAN names it.
func TestWindowsScanOfARealFolder(t *testing.T) {
	root := os.Getenv("FILEES_ANCHOR_REPAIR_SCAN")
	if root == "" {
		t.Skip("set FILEES_ANCHOR_REPAIR_SCAN to a virtual folder to inspect it")
	}
	system := Windows{}
	provider, err := system.SyncRootProvider(root)
	t.Logf("provider %q err %v elevated %v", provider, err, Elevated())
	damaged, err := Scan(system, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d unreadable", len(damaged))
	for _, entry := range damaged {
		t.Logf("  dir=%v %s", entry.Directory, entry.Path)
	}
}
