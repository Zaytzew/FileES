package clientprofile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The failure this exists to prevent, in the words the user saw:
// "create server state root: mkdir ...\servers\atmprojekt:filees: The
// directory name is invalid". A colon is ordinary in an identifier and Unix
// stores it without complaint; on Windows it separates a drive letter and an
// alternate data stream, so activation died at the first mkdir.
func TestIdentifiersTheFilesystemReservesAreEncoded(t *testing.T) {
	name, err := StateDirName("atmprojekt:filees")
	if err != nil {
		t.Fatal(err)
	}
	if name == "atmprojekt:filees" {
		t.Fatal("a colon must not reach the filesystem; it is reserved on Windows")
	}
	if name != "atmprojekt%3Afilees" {
		t.Fatalf("name = %q", name)
	}

	// It has to actually work where it failed, not merely look different.
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("the encoded name must be creatable on this platform: %v", err)
	}
}

// Every server already in use keeps its directory. Renaming them would have
// orphaned live installations for the sake of an identifier none of them use.
func TestExistingIdentifiersAreUnchanged(t *testing.T) {
	for _, id := range []string{"spot", "manual", "cloud-1", "atm_projekt", "a.b.c"} {
		name, err := StateDirName(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if name != id {
			t.Fatalf("%q was renamed to %q; encoding must be identity for names already in use", id, name)
		}
	}
}

// Two servers must never share a directory. Without encoding the percent sign
// the identifier "a%3Ab" and the identifier "a:b" would collide, and the
// second one to activate would silently adopt the first one's keys and cache.
func TestEncodingIsInjective(t *testing.T) {
	colon, err := StateDirName("a:b")
	if err != nil {
		t.Fatal(err)
	}
	literal, err := StateDirName("a%3Ab")
	if err != nil {
		t.Fatal(err)
	}
	if colon == literal {
		t.Fatalf("two different identifiers share a directory: %q", colon)
	}
}

// Windows reserves device names with or without an extension and ignores case,
// and it strips a trailing dot or space, so a name ending in one resolves
// somewhere else than requested.
func TestWindowsSpecialNamesAreDefused(t *testing.T) {
	for _, id := range []string{"CON", "con", "nul.txt", "aux", "COM1", "LPT9"} {
		name, err := StateDirName(id)
		if err != nil {
			t.Fatal(err)
		}
		if name == id {
			t.Fatalf("%q is a reserved device name and must not be used as a directory", id)
		}
	}
	for _, id := range []string{"serwer.", "serwer "} {
		name, err := StateDirName(id)
		if err != nil {
			t.Fatal(err)
		}
		if name == id {
			t.Fatalf("%q ends in a character Windows strips, so it must be encoded", id)
		}
	}
}

// The same name on every platform. Deciding per-OS would give one server two
// different directories depending on where its client runs, and this product
// moves working copies between machines.
func TestEncodingDoesNotDependOnTheHost(t *testing.T) {
	name, err := StateDirName("atmprojekt:filees")
	if err != nil {
		t.Fatal(err)
	}
	if name != "atmprojekt%3Afilees" {
		t.Fatalf("on %s the name came out as %q", runtime.GOOS, name)
	}
}
