package privatefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirProducesAPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "servers")
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("Verify after EnsureDir: %v", err)
	}
}

// EnsureDir has to narrow a directory that already exists. MkdirAll succeeds
// silently in that case without touching permissions, so an inherited
// world-readable directory would otherwise be accepted as private.
func TestEnsureDirNarrowsAnExistingWideDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inherited")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	widen(t, dir)
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("Verify after re-running EnsureDir: %v", err)
	}
}

func TestHardenMakesAFilePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconnect_ed25519")
	if err := os.WriteFile(path, []byte("key material"), 0o600); err != nil {
		t.Fatal(err)
	}
	widen(t, path)
	if err := Harden(path); err != nil {
		t.Fatalf("Harden: %v", err)
	}
	if err := Verify(path); err != nil {
		t.Fatalf("Verify after Harden: %v", err)
	}
	// Hardening must not disturb the contents it protects.
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "key material" {
		t.Fatalf("content after Harden = %q, %v", raw, err)
	}
}

// The test that matters: a file anyone can reach must be rejected. Without
// this, Verify could return nil unconditionally and every other test here
// would still pass.
func TestVerifyRejectsAWorldAccessiblePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pin.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Note what is deliberately absent here: any assumption that the 0600
	// argument above made the file private. On Windows it does not — the file
	// inherits its parent's DACL, and on the machine this was written on that
	// inheritance handed full access to a second local account. Harden is what
	// establishes the guarantee, on every platform.
	if err := Harden(path); err != nil {
		t.Fatalf("Harden: %v", err)
	}
	if err := Verify(path); err != nil {
		t.Fatalf("Verify after Harden: %v", err)
	}
	widen(t, path)
	err := Verify(path)
	if !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("Verify on a world-accessible file = %v, want ErrNotPrivate", err)
	}
}

func TestVerifyReportsMissingPathsAsSomethingElse(t *testing.T) {
	err := Verify(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("Verify on a missing path returned nil")
	}
	// A caller creating the file for the first time must be able to tell
	// "not there yet" apart from "there and exposed".
	if errors.Is(err, ErrNotPrivate) {
		t.Fatalf("missing path reported as ErrNotPrivate: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Verify on a missing path = %v, want os.ErrNotExist", err)
	}
}
