//go:build !windows

package storage

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
)

func TestCapacityIs64BitAndLeavesHeadroom(t *testing.T) {
	if CheckSpace(4<<30, 5<<30) == nil {
		t.Fatal("4 GiB filesystem admitted 5 GiB")
	}
	if err := CheckSpace(20<<30, 10<<30); err != nil {
		t.Fatal(err)
	}
	if CheckSpace(math.MaxInt64, math.MaxInt64) == nil {
		t.Fatal("overflow admitted")
	}
	if product(math.MaxUint64, 4096) != math.MaxInt64 {
		t.Fatal("overflow")
	}
	if !errors.Is(CheckSpace(0, 1), ErrUnavailable) {
		t.Fatal("lost storage classification")
	}
}
func TestActualFilesystemAndMissingRoot(t *testing.T) {
	root := t.TempDir()
	if err := RequireSpace(root, 1); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(RequireSpace(filepath.Join(root, "absent"), 1), ErrUnavailable) {
		t.Fatal("missing root was usable")
	}
}
func TestPersistentRootRejectsTemporaryAndBroadPaths(t *testing.T) {
	for _, path := range []string{"/", "relative", "/tmp/downloads", "/var/tmp/downloads"} {
		if PersistentRoot(path) == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}
