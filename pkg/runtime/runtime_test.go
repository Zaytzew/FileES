package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRepoMutexReclaimsDeadOwner(t *testing.T) {
	base := t.TempDir()
	m := &repoMutex{baseDir: base}
	dir := filepath.Join(base, hash("repo"))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	owner := fmt.Sprintf("%d 1 1\n", deadPID())
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := m.Lock(ctx, "repo")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	unlock()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("lock directory remains after unlock: %v", err)
	}
}

func TestHostGateReclaimsDeadOwner(t *testing.T) {
	base := t.TempDir()
	g := &hostGate{baseDir: base, k: 1}
	dir := filepath.Join(base, "slot.1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	owner := fmt.Sprintf("%d 1 1\n", deadPID())
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := g.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("slot directory remains after release: %v", err)
	}
}

func TestRepoMutexDoesNotReclaimLiveOwner(t *testing.T) {
	base := t.TempDir()
	m := &repoMutex{baseDir: base}
	dir := filepath.Join(base, hash("repo"))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	owner := fmt.Sprintf("%d 1 1\n", os.Getpid())
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.Lock(ctx, "repo"); err != context.DeadlineExceeded {
		t.Fatalf("Lock error = %v, want deadline exceeded", err)
	}
}

func TestV2OwnerLockRejectsPIDReuseFalsePositive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A live but unrelated process may have reused the dead owner's PID. With no
	// advisory lock held on this v2 owner file, the directory is still stale.
	owner := fmt.Sprintf("v2 %d 1 1\n", os.Getpid())
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	if stale, err := staleLockDir(dir, time.Now()); err != nil || !stale {
		t.Fatalf("reused PID owner: stale=%v err=%v", stale, err)
	}
}

func TestV2OwnerLockRemainsLiveWhileDescriptorIsHeld(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lock")
	release, acquired, err := tryLockDir(dir)
	if err != nil || !acquired {
		t.Fatalf("tryLockDir: acquired=%v err=%v", acquired, err)
	}
	if stale, err := staleLockDir(dir, time.Now()); err != nil || stale {
		t.Fatalf("held owner lock: stale=%v err=%v", stale, err)
	}
	release()
}

func TestLegacyLockRequiresGracePeriod(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if stale, err := staleLockDir(dir, time.Now()); err != nil || stale {
		t.Fatalf("fresh legacy lock: stale=%v err=%v", stale, err)
	}
	old := time.Now().Add(-legacyLockGrace - time.Second)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	if stale, err := staleLockDir(dir, time.Now()); err != nil || !stale {
		t.Fatalf("old legacy lock: stale=%v err=%v", stale, err)
	}
}

func TestOldReleaseCannotRemoveNewOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseLockDir(dir, "old\n", nil)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("new owner's lock was removed: %v", err)
	}
}

func deadPID() int {
	for pid := 1 << 30; pid > 0; pid-- {
		if !processAlive(pid) {
			return pid
		}
	}
	panic("could not find a dead pid")
}
