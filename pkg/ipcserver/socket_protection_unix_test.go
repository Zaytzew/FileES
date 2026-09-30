//go:build !windows

package ipcserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSocketRefusesSharedDirectoryWithoutChangingPermissions(t *testing.T) {
	path := protectedTestSocket(t)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if listener, _, err := listenProtectedSocket(path, protectSocket); err == nil {
		listener.Close()
		t.Fatal("shared IPC directory accepted")
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("shared directory permissions changed: %v, %v", info, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("listener created before directory check: %v", err)
	}
}
