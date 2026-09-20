//go:build linux

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLinuxNativeCancellationStopsTunnel(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "helper")
	marker := filepath.Join(root, "child-pid")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nsleep 30 &\necho $! > \"$1\"\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := New(Options{NativeSVNPath: helper}).(*execClient)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := c.nativeCommand(ctx, "", 10*time.Second, marker); result <- err }()
	var raw []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ = os.ReadFile(marker)
		if strings.TrimSpace(string(raw)) != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("child did not start", err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not drain")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, err = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if os.IsNotExist(err) {
			return
		}
		// A killed orphan may wait for PID 1 to reap it; it cannot keep the tunnel.
		fields := strings.Fields(string(raw))
		if len(fields) > 2 && fields[2] == "Z" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native cancellation left a live tunnel descendant", pid)
}
