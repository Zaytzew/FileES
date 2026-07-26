//go:build !windows

package activation

import (
	"context"
	"testing"
	"time"
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
