package ipcserver

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/privatefile"
)

func protectedTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := privatefile.Harden(dir); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "ipc.sock")
}

func TestSocketProtectionFailureClosesAndRemovesListener(t *testing.T) {
	path := protectedTestSocket(t)
	want := errors.New("injected permission failure")
	listener, _, err := listenProtectedSocket(path, func(string) error { return want })
	if !errors.Is(err, want) || listener != nil {
		t.Fatalf("listener=%v, error=%v", listener, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed socket was not removed: %v", err)
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()
		t.Fatal("failed listener still accepts connections")
	}
}

func TestSocketProtectionUsesNativePermissions(t *testing.T) {
	path := protectedTestSocket(t)
	listener, owned, err := listenProtectedSocket(path, protectSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSocketIfOwned(path, owned)
	defer listener.Close()
	if err := privatefile.Verify(path); err != nil {
		t.Fatalf("socket is not private: %v", err)
	}
}

func TestSocketCreatesPrivateParent(t *testing.T) {
	path := filepath.Join(filepath.Dir(protectedTestSocket(t)), "new", "ipc.sock")
	listener, owned, err := listenProtectedSocket(path, protectSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSocketIfOwned(path, owned)
	defer listener.Close()
	if err := privatefile.Verify(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
}

func TestSocketHardensExistingDefaultStateDirectory(t *testing.T) {
	home := filepath.Dir(protectedTestSocket(t))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".filees", "ipc.sock")
	if err := os.Mkdir(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	listener, owned, err := listenProtectedSocket(path, protectSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSocketIfOwned(path, owned)
	defer listener.Close()
	if err := privatefile.Verify(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
}

func TestSocketProtectionFailurePreservesReplacement(t *testing.T) {
	path := protectedTestSocket(t)
	want := errors.New("injected failure after replacement")
	var replacement net.Listener
	_, _, err := listenProtectedSocket(path, func(string) error {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		var err error
		replacement, err = net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		return want
	})
	defer replacement.Close()
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement removed: %v", err)
	}
}
