package nativeruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLeasePinsConcurrentVersionsAndCollectsAfterLastUser(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	one, two := fixture(t, "one"), fixture(t, "two")
	a, err := Acquire(t.Context(), cache, one)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Acquire(t.Context(), cache, two)
	if err != nil {
		t.Fatal(err)
	}
	another, err := Acquire(t.Context(), cache, two)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := Sweep(t.Context(), cache); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	b.Close()
	if n, err := Sweep(t.Context(), cache); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	another.Close()
	if n, err := Sweep(t.Context(), cache); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(a.Path); err != nil {
		t.Fatal(err)
	}
	// The next use verifies/reinstalls the removed version under the same pin.
	b, err = Acquire(t.Context(), cache, two)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
}

func TestLeaseSweepKeepsLegacyAndUnknownContents(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	payload := fixture(t, "old")
	legacy, err := Ensure(cache, payload)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := Acquire(t.Context(), cache, payload)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	root := filepath.Join(cache, managedDirectory)
	unknown := filepath.Join(root, "user-data")
	if err := os.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(lease.Path), "user-data.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := Sweep(t.Context(), cache); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal(err)
	}
}

// The child waits with an inherited pin after its parent handle has closed.
func TestRuntimePinChild(t *testing.T) {
	if os.Getenv("FILEES_RUNTIME_PIN_TEST") != "1" {
		return
	}
	marker := os.Getenv("FILEES_RUNTIME_PIN_MARKER")
	if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
		os.Exit(4)
	}
	for i := 0; i < 600; i++ {
		if _, err := os.Stat(marker + ".exit"); err == nil {
			os.Exit(0)
		}
		time.Sleep(20 * time.Millisecond)
	}
	os.Exit(5)
}

func TestInheritedPinOutlivesParentHandle(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	lease, err := Acquire(t.Context(), cache, fixture(t, "child"))
	if err != nil {
		t.Fatal(err)
	}
	// Inheritance is independent of child image; the test process substitutes
	// the helper while exercising the same PinCommand/Start protocol.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, lease.Path, "-test.run=^TestRuntimePinChild$")
	release, err := PinCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Path = executable
	cmd.Args[0] = executable
	marker := filepath.Join(t.TempDir(), "started")
	cmd.Env = append(os.Environ(), "FILEES_RUNTIME_PIN_TEST=1", "FILEES_RUNTIME_PIN_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		release()
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	release()
	lease.Close()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if n, err := Sweep(t.Context(), cache); n != 0 || err != nil {
		t.Fatal("child lost inherited pin", n, err)
	}
	if err := os.WriteFile(marker+".exit", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if n, err := Sweep(t.Context(), cache); n != 1 || err != nil {
		t.Fatal(n, err)
	}
}

func TestLeaseSweepRemovesAbandonedStagingAndKeepsSymlinkTarget(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	lease, err := Acquire(t.Context(), cache, fixture(t, "live"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	root := filepath.Join(cache, managedDirectory)
	stage := filepath.Join(root, ".staging-crashed")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, Executable), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".staging-symlink")); err != nil {
		t.Log("symlink unavailable:", err)
	}
	if n, err := Sweep(t.Context(), cache); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.Path); err != nil {
		t.Fatal(err)
	}
}
