//go:build !windows

package servertool

import (
	"context"
	"io"
	"testing"
	"time"

	"filees/internal/obsandbox"
)

// Real OS pipes, FIFO and process-group termination. The native unveil profile
// is separately tested: applying it to the test runner would restrict every
// subsequent test and its temporary-directory cleanup.
func TestGrantBarrierWaitsForSupervisorChildExit(t *testing.T) {
	manager, config, grant, lease := newSupervisorTestSession(t)
	originalSandbox := sandboxApply
	sandboxApply = func(obsandbox.Profile) error { return nil }
	t.Cleanup(func() { sandboxApply = originalSandbox; _ = lease.Close() })
	input, keepOpen := io.Pipe()
	defer keepOpen.Close()
	ready := make(sessionReadyWriter, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runSessionSupervisor(config, grant.ClientID, config.Activation.ServiceRepository, manager, lease, input, ready, io.Discard, sessionRelayTestChild)
		if closeErr := lease.Close(); err == nil {
			err = closeErr
		}
		done <- err
	}()
	go func() { _, _ = keepOpen.Write([]byte("READY\n")) }()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not start")
	}
	childEnded := false
	err := manager.WithRealmSessionsStopped(context.Background(), grant.RealmID, func() error {
		select {
		case err := <-done:
			if err != nil {
				return err
			}
			childEnded = true
		case <-time.After(time.Second):
			t.Error("published before supervisor ended")
		}
		return nil
	})
	if err != nil || !childEnded {
		t.Fatalf("barrier err=%v childEnded=%v", err, childEnded)
	}
	if !manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("grant barrier revoked whole activation")
	}
	newLease, err := manager.ClaimSession(grant.OperationID, grant.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	_ = newLease.Close()
}
