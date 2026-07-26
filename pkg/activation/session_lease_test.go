//go:build !windows

package activation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	for _, name := range []string{sessionRecordName, sessionFIFOName} {
		if _, err := os.Lstat(dir + "/" + name); err != nil {
			t.Fatalf("known lease artifact %s was removed before unknown content was detected: %v", name, err)
		}
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

func TestCleanupOrphanedSessionLeasesPreservesOldLiveLease(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := newTestSessionLeaseAt(t, root, time.Now().Add(-72*time.Hour))
	defer func() { _ = lease.Close() }()
	cleaned, err := cleanupOrphanedSessionLeases(root)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 0 {
		t.Fatalf("cleaned live leases=%d, want 0", cleaned)
	}
	if _, err := os.Stat(lease.Dir); err != nil {
		t.Fatalf("old but live lease was removed: %v", err)
	}
	revoked, err := lease.Revoked()
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Fatal("liveness probe wrote a revoke marker")
	}
}

func TestCleanupOrphanedSessionLeasesRemovesLeaseWithoutReader(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := newTestSessionLeaseAt(t, root, time.Now())
	dir := lease.Dir
	if err := lease.fifo.Close(); err != nil {
		t.Fatal(err)
	}
	lease.fifo = nil
	cleaned, err := cleanupOrphanedSessionLeases(root)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned orphaned leases=%d, want 1", cleaned)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("orphaned lease still exists: %v", err)
	}
}

func TestCleanupOrphanedSessionLeasesRemovesInterruptedCreation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := newTestSessionLeaseAt(t, root, time.Now())
	dir := lease.Dir
	if err := lease.fifo.Close(); err != nil {
		t.Fatal(err)
	}
	lease.fifo = nil
	if err := os.Remove(filepath.Join(dir, sessionFIFOName)); err != nil {
		t.Fatal(err)
	}
	cleaned, err := cleanupOrphanedSessionLeases(root)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned interrupted leases=%d, want 1", cleaned)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("interrupted lease still exists: %v", err)
	}
}

func TestCleanupOrphanedSessionLeasesFailsClosedOnUnknownArtifact(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := newTestSessionLeaseAt(t, root, time.Now())
	defer func() { _ = lease.Close() }()
	if err := lease.fifo.Close(); err != nil {
		t.Fatal(err)
	}
	lease.fifo = nil
	unknown := filepath.Join(lease.Dir, "unexpected")
	if err := os.WriteFile(unknown, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleaned, err := cleanupOrphanedSessionLeases(root)
	if err == nil {
		t.Fatal("cleanup accepted an unknown lease artifact")
	}
	if cleaned != 0 {
		t.Fatalf("cleaned unsafe leases=%d, want 0", cleaned)
	}
	for _, path := range []string{lease.Dir, unknown, filepath.Join(lease.Dir, sessionRecordName), filepath.Join(lease.Dir, sessionFIFOName)} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("fail-closed cleanup removed %s: %v", path, err)
		}
	}
}

func TestCleanupOrphanedSessionLeasesSharesClaimLock(t *testing.T) {
	manager, config := newActivationTestManager(t)
	var lease *SessionLease
	cleanupDone := make(chan error, 1)
	lockPath := filepath.Join(config.Root, ".activation.lock")
	err := withFileLock(lockPath, func() error {
		var err error
		lease, err = createSessionLease(manager.sessionRoot(), SessionMetadata{
			OperationID: uuid.NewString(),
			ClientID:    uuid.NewString(),
			RealmID:     uuid.NewString(),
			StartedAt:   time.Now().Add(-72 * time.Hour),
		})
		if err != nil {
			return err
		}
		go func() {
			cleaned, err := manager.CleanupOrphanedSessionLeases()
			if err == nil && cleaned != 0 {
				err = errors.New("cleanup removed a live lease")
			}
			cleanupDone <- err
		}()
		select {
		case err := <-cleanupDone:
			return fmt.Errorf("cleanup bypassed activation lock: %v", err)
		case <-time.After(100 * time.Millisecond):
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not continue after activation lock was released")
	}
}

func newTestSessionLease(t *testing.T) *SessionLease {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return newTestSessionLeaseAt(t, root, time.Now())
}

func newTestSessionLeaseAt(t *testing.T, root string, startedAt time.Time) *SessionLease {
	t.Helper()
	lease, err := createSessionLease(root, SessionMetadata{
		OperationID: uuid.NewString(),
		ClientID:    uuid.NewString(),
		RealmID:     uuid.NewString(),
		StartedAt:   startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
