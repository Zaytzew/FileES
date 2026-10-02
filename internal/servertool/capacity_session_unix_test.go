//go:build linux || openbsd

package servertool

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/storagewatch"
)

func TestGlobalCapacityBarrierWaitsForRealSupervisorAndKeepsGrant(t *testing.T) {
	manager, config, grant, lease := newSupervisorTestSession(t)
	previous := sandboxApply
	sandboxApply = func(obsandbox.Profile) error { return nil }
	t.Cleanup(func() { sandboxApply = previous; _ = lease.Close() })
	in, input := io.Pipe()
	defer input.Close()
	ready, done := make(sessionReadyWriter, 1), make(chan error, 1)
	go func() {
		_, err := runSessionSupervisor(config, grant.ClientID, config.Activation.ServiceRepository, manager, lease, in, ready, io.Discard, sessionRelayTestChild)
		if closeErr := lease.Close(); err == nil {
			err = closeErr
		}
		done <- err
	}()
	go func() { _, _ = input.Write([]byte("READY\n")) }()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.SignalAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.WithAllSessionsStopped(ctx, func() error {
		// Lease cleanup proves child completion; no PID-based heuristic.
		if _, err := os.Stat(lease.Dir); !os.IsNotExist(err) {
			t.Fatal("unacknowledged child", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("capacity stop changed grant")
	}
}

func TestSVNChildReadOnlyModeUsesPersistentCapacityPolicy(t *testing.T) {
	_, config, grant, lease := newSupervisorTestSession(t)
	defer lease.Close()
	config.Activation.ClientEntryPath = "/bin/true"
	gate := storagewatch.Gate{Root: config.Activation.Root}
	for _, held := range []bool{false, true, false} {
		if held {
			if err := gate.Hold(); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := gate.Resume(); err != nil {
				t.Fatal(err)
			}
		}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		command, err := startSessionChild(config, grant.ClientID, config.Activation.ServiceRepository, "nonce", r, nil, nil, nil)
		r.Close()
		w.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
		last := command.Args[len(command.Args)-1]
		if (last == "read-only") != held {
			t.Fatal(command.Args, held)
		}
	}
	if err := os.WriteFile(filepath.Join(config.Activation.Root, "capacity-write-policy.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	command, err := startSessionChild(config, grant.ClientID, config.Activation.ServiceRepository, "nonce", r, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if command.Args[len(command.Args)-1] != "read-only" {
		t.Fatal("unreadable policy admitted native writer")
	}
}
