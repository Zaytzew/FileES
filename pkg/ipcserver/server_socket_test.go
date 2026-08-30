package ipcserver_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/ipcserver"
)

func TestServerStartDoesNotReplaceLiveSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "filees.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ipcserver.New(socket).Start(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := ipcserver.New(socket).Start(context.Background()); err == nil {
		t.Fatal("second daemon replaced a live IPC socket")
	}
	after, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("live IPC socket identity changed")
	}
}

func TestServerStartReplacesStaleSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "filees.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ipcserver.New(socket).Start(ctx); err != nil {
		t.Fatalf("start with stale socket: %v", err)
	}
}

func TestStoppingOldServerDoesNotRemoveReplacementSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "filees.sock")
	ctx, cancel := context.WithCancel(context.Background())
	if err := ipcserver.New(socket).Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		t.Fatal("old daemon removed the replacement IPC socket")
	}
}
