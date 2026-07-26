//go:build !windows

package activation

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRevokeSignalsClaimedSessionLease(t *testing.T) {
	manager, _ := newActivationTestManager(t)
	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	lease, err := manager.ClaimSession(grant.OperationID, grant.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if !manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("freshly claimed active session was not allowed")
	}
	if _, err := manager.Revoke(context.Background(), grant.ClientID, "session test"); err != nil {
		t.Fatal(err)
	}
	revoked, err := lease.Revoked()
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("revoke did not notify the matching session FIFO")
	}
	if manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("revoked session remained allowed")
	}
}

func TestRevokeSignalsStagedSessionLease(t *testing.T) {
	manager, _ := newActivationTestManager(t)
	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	lease, err := manager.ClaimSession(grant.OperationID, grant.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if !manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("freshly claimed staged session was not allowed")
	}
	if _, err := manager.Revoke(context.Background(), grant.ClientID, "staged session test"); err != nil {
		t.Fatal(err)
	}
	revoked, err := lease.Revoked()
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("revoke did not notify the staged session FIFO")
	}
	if manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("revoked staged session remained allowed")
	}
}

func TestSessionLeaseCloseRemovesOnlyKnownArtifacts(t *testing.T) {
	lease := newTestSessionLease(t)
	dir := lease.Dir
	if err := os.WriteFile(dir+"/unexpected", []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err == nil {
		t.Fatal("session lease cleanup unexpectedly removed a directory with an unknown artifact")
	}
	if _, err := os.Stat(dir + "/unexpected"); err != nil {
		t.Fatalf("unknown lease artifact was not preserved: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("lease directory was not preserved after fail-closed cleanup: %v", err)
	}
}

func TestSessionLeaseCloseRemovesKnownArtifacts(t *testing.T) {
	lease := newTestSessionLease(t)
	dir := lease.Dir
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("closed lease directory still exists: %v", err)
	}
}

func TestFreshSessionLeaseRevokedDoesNotBlock(t *testing.T) {
	lease := newTestSessionLease(t)
	defer func() { _ = lease.Close() }()
	type result struct {
		revoked bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		revoked, err := lease.Revoked()
		done <- result{revoked: revoked, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.revoked {
			t.Fatal("fresh session lease unexpectedly reported revoke")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fresh session lease revoke poll blocked instead of returning EAGAIN")
	}
}

func newTestSessionLease(t *testing.T) *SessionLease {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lease, err := createSessionLease(root, SessionMetadata{
		OperationID: uuid.NewString(),
		ClientID:    uuid.NewString(),
		RealmID:     uuid.NewString(),
		StartedAt:   time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
