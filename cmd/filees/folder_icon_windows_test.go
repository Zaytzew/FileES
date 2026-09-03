//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"filees/pkg/filepolicy"

	"golang.org/x/sys/windows"
)

func attributesOf(t *testing.T, path string) uint32 {
	t.Helper()
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		t.Fatalf("attributes of %s: %v", path, err)
	}
	return attrs
}

func decodeUTF16LE(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Fatalf("desktop.ini has no UTF-16LE BOM; the shell would read it through the system codepage")
	}
	units := make([]uint16, 0, (len(raw)-2)/2)
	for i := 2; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

func TestAManagedFolderGetsTheIconWithoutBecomingUnwritable(t *testing.T) {
	root := t.TempDir()
	icon := filepath.Join(t.TempDir(), "filees-folder.ico")
	if err := os.WriteFile(icon, []byte("ico"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := markManagedFolder(root, icon); err != nil {
		t.Fatalf("markManagedFolder: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "desktop.ini"))
	if err != nil {
		t.Fatalf("read desktop.ini: %v", err)
	}
	content := decodeUTF16LE(t, raw)
	if !strings.Contains(content, "[.ShellClassInfo]") || !strings.Contains(content, "IconResource="+icon+",0") {
		t.Fatalf("desktop.ini = %q", content)
	}

	// Hidden and system, so the file does not sit among the owner's drawings.
	iniAttrs := attributesOf(t, filepath.Join(root, "desktop.ini"))
	if iniAttrs&windows.FILE_ATTRIBUTE_HIDDEN == 0 || iniAttrs&windows.FILE_ATTRIBUTE_SYSTEM == 0 {
		t.Errorf("desktop.ini attributes = %#x, want hidden and system", iniAttrs)
	}
	// READONLY on a directory is Explorer's "this folder is customised" flag,
	// not a permission. If it ever became one, every commit into every managed
	// working copy would stop - so this is asserted, not assumed.
	rootAttrs := attributesOf(t, root)
	if rootAttrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
		t.Errorf("folder attributes = %#x, want readonly", rootAttrs)
	}
	probe := filepath.Join(root, "rysunek.dwg")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatalf("write inside a marked folder: %v", err)
	}
	if err := os.Rename(probe, filepath.Join(root, "rysunek-2.dwg")); err != nil {
		t.Fatalf("rename inside a marked folder: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "rysunek-2.dwg")); err != nil {
		t.Fatalf("delete inside a marked folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "OPRACOWANIE"), 0o755); err != nil {
		t.Fatalf("create a subdirectory in a marked folder: %v", err)
	}
}

func TestMarkingIsIdempotentAndRewritesNothingUnchanged(t *testing.T) {
	root := t.TempDir()
	icon := filepath.Join(t.TempDir(), "filees-folder.ico")
	if err := os.WriteFile(icon, []byte("ico"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := markManagedFolder(root, icon); err != nil {
		t.Fatalf("first mark: %v", err)
	}
	desktopINI := filepath.Join(root, "desktop.ini")
	before, err := os.Stat(desktopINI)
	if err != nil {
		t.Fatal(err)
	}
	// This runs on every repository start. Rewriting an identical file each
	// time invalidates the shell's icon cache, which the owner sees as folders
	// flickering back to plain on every daemon restart.
	if err := markManagedFolder(root, icon); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	after, err := os.Stat(desktopINI)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("desktop.ini was rewritten although nothing changed")
	}
}

func TestADetachedFolderStopsLookingLikeOurs(t *testing.T) {
	root := t.TempDir()
	icon := filepath.Join(t.TempDir(), "filees-folder.ico")
	if err := os.WriteFile(icon, []byte("ico"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := markManagedFolder(root, icon); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "rysunek.dwg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unmarkManagedFolder(root); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "desktop.ini")); !os.IsNotExist(err) {
		t.Errorf("desktop.ini survived the detach: %v", err)
	}
	if attrs := attributesOf(t, root); attrs&windows.FILE_ATTRIBUTE_READONLY != 0 {
		t.Errorf("folder attributes = %#x, still marked customised", attrs)
	}
	// The owner's files stay exactly where they are. Only the decoration goes.
	if _, err := os.Stat(filepath.Join(root, "rysunek.dwg")); err != nil {
		t.Errorf("the owner's file did not survive unmarking: %v", err)
	}
}

func TestUnmarkingAFolderWeNeverMarkedIsHarmless(t *testing.T) {
	if err := unmarkManagedFolder(t.TempDir()); err != nil {
		t.Fatalf("unmark: %v", err)
	}
}

func TestMarkingRefusesRelativePaths(t *testing.T) {
	if err := markManagedFolder("relative", `C:\icon.ico`); err == nil {
		t.Error("a relative root was accepted")
	}
	if err := markManagedFolder(t.TempDir(), "icon.ico"); err == nil {
		t.Error("a relative icon path was accepted; Explorer stores the path and would resolve it against its own directory")
	}
}

// Customising a whole volume is not a decoration, and clearing attributes on
// one because a path was malformed is damage rather than tidy-up.
func TestNeitherDirectionTouchesAVolumeRoot(t *testing.T) {
	for _, root := range []string{`C:\`, `D:\`, `\\server\share`} {
		if err := markManagedFolder(root, `C:\icon.ico`); err == nil {
			t.Errorf("markManagedFolder(%q) was accepted", root)
		}
		if err := unmarkManagedFolder(root); err == nil {
			t.Errorf("unmarkManagedFolder(%q) was accepted", root)
		}
	}
}

// The file this writes lands inside a directory whose entire purpose is that
// its contents get versioned. If the ignore rule ever went, every managed
// working copy on Windows would start committing a local presentation detail -
// and the path in it differs per machine, so it would conflict immediately.
func TestDesktopIniIsRefusedByThePolicyThatProtectsThisFromBeingCommitted(t *testing.T) {
	for _, rel := range []string{"desktop.ini", "OPRACOWANIE/desktop.ini"} {
		if !filepolicy.IsBuiltinIgnored(rel) {
			t.Fatalf("%s is not ignored; the folder icon would be committed", rel)
		}
	}
}
