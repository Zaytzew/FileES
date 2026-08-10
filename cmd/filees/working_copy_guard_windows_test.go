//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkingCopyGuardBlocksOutOfBandRootRename(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "wc")
	moved := filepath.Join(parent, "wc-moved")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	guard, err := acquireWorkingCopyGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, moved); err == nil {
		_ = guard.Close()
		t.Fatal("working-copy root was renamed while guard was active")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, moved); err != nil {
		t.Fatalf("working-copy root remained pinned after guard close: %v", err)
	}
	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}
}
