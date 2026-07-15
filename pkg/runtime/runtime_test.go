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

// ---- Klasa 4: równoległe SIGKILL wszystkich posiadaczy K-slotów ----

// TestHostGateReclaimsBothSlotsWhenAllHoldersDie simulates the scenario where
// all K slot-holders are killed simultaneously (SIGKILL). A new acquirer must
// be able to reclaim both stale lockdirs and acquire a slot within one polling
// cycle.
func TestHostGateReclaimsBothSlotsWhenAllHoldersDie(t *testing.T) {
	base := t.TempDir()
	g := &hostGate{baseDir: base, k: 2}

	// Both slots are occupied by dead owners.
	dead := deadPID()
	for _, slot := range []string{"slot.1", "slot.2"} {
		dir := filepath.Join(base, slot)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		owner := fmt.Sprintf("%d 1 1\n", dead)
		if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(owner), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := g.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire with two dead-owner slots: %v", err)
	}
	release()

	// After release the slot directory must be gone.
	entries, _ := os.ReadDir(base)
	var slotDirs []string
	for _, e := range entries {
		if e.IsDir() {
			slotDirs = append(slotDirs, e.Name())
		}
	}
	if len(slotDirs) != 0 {
		t.Fatalf("slot directories remain after release: %v", slotDirs)
	}
}

// TestConcurrentAcquirersRaceToReclaimStaleSlot places two goroutines in a
// race to reclaim the same stale lockdir. Exactly one must win the rename
// (tryLockDir is atomic on the rename), the other must wait and then succeed
// after the winner releases.
func TestConcurrentAcquirersRaceToReclaimStaleSlot(t *testing.T) {
	base := t.TempDir()
	g := &hostGate{baseDir: base, k: 1}

	dir := filepath.Join(base, "slot.1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dead := deadPID()
	owner := fmt.Sprintf("%d 1 1\n", dead)
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type result struct {
		release func()
		err     error
	}
	ch := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			release, err := g.Acquire(ctx)
			ch <- result{release, err}
		}()
	}

	// First result: whichever goroutine wins the reclaim race.
	r1 := <-ch
	if r1.err != nil {
		t.Fatalf("first acquirer: %v", r1.err)
	}
	// Release the slot so the second goroutine can proceed.
	r1.release()

	r2 := <-ch
	if r2.err != nil {
		t.Fatalf("second acquirer after first released: %v", r2.err)
	}
	r2.release()
}
