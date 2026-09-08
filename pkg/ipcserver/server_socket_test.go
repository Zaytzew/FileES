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
	socket := shortSocketPath(t)
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
	socket := shortSocketPath(t)
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

// shortSocketPath keeps a unix socket inside the length the kernel accepts.
//
// t.TempDir() embeds the full test name, so a descriptive name pushes the
// socket past the sockaddr_un limit and the test fails for its own name rather
// than for the product. Measured 2026-09-08: this file passed under a short
// TEMP and failed under the default one, which made it look environmental and
// kept it on the known-failures list for a day.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "filees.sock")
}

func TestStoppingOldServerDoesNotRemoveReplacementSocket(t *testing.T) {
	socket := shortSocketPath(t)
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
