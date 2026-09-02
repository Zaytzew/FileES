//go:build !windows

package privatefile

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestVerifyUsesEffectiveFilesystemIdentity(t *testing.T) {
	path := t.TempDir()
	// Establish the precondition rather than assume it: t.TempDir() is not
	// private on every platform - on OpenBSD it comes back 0755, so Verify
	// refused on the mode long before reaching the ownership check this test
	// is actually about, and the test could never pass there.
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("temporary directory exposes no Unix ownership")
	}

	original := effectiveUID
	t.Cleanup(func() { effectiveUID = original })
	effectiveUID = func() int { return int(stat.Uid) }
	if err := Verify(path); err != nil {
		t.Fatalf("effective owner rejected: %v", err)
	}

	effectiveUID = func() int { return int(stat.Uid) + 1 }
	if err := Verify(path); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("foreign effective owner error = %v", err)
	}
}

// widen exposes the path to everyone, so the shared tests can prove Verify
// actually rejects it.
func widen(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o644)
	if info.IsDir() {
		mode = 0o755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// Root may read anything, so demanding that root also *own* the path asserts
// nothing about privacy while breaking every administrative tool that legitimately
// inspects a service account's directory. filees-admin, run the ordinary way,
// refused to start against /var/filees/onboarding for exactly this reason.
//
// The mode check still applies to root: a genuinely exposed path is still
// rejected, which is the guarantee that actually matters.
func TestVerifyAcceptsRootForAnotherAccountsPrivatePath(t *testing.T) {
	path := t.TempDir()
	// t.TempDir() is not private everywhere - on OpenBSD it comes back 0755 -
	// so the precondition is established explicitly rather than assumed.
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("temporary directory exposes no Unix ownership")
	}

	original := effectiveUID
	t.Cleanup(func() { effectiveUID = original })

	// A non-root caller that does not own the path is still refused...
	effectiveUID = func() int { return int(stat.Uid) + 1 }
	if err := Verify(path); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("foreign non-root caller was accepted: %v", err)
	}

	// ...while root inspecting the same private path is not.
	effectiveUID = func() int { return 0 }
	if err := Verify(path); err != nil {
		t.Fatalf("root rejected on a private path owned by another account: %v", err)
	}

	// Root must not be a blanket bypass: an exposed path stays rejected.
	widen(t, path)
	if err := Verify(path); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("root accepted a world-accessible path: %v", err)
	}
}
