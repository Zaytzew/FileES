package passport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutoUnlockOwnedPreservesMetadataAndUnmanagedFiles(t *testing.T) {
	now := time.Now()
	b := newFakeBackend()
	b.needsLock = map[string]bool{"managed.txt": true, ".svn/internal": true, ".filees/internal": true, "link.txt": true, "linked-dir/outside.txt": true}
	m := openTestManager(t, b, &now, Config{})
	wc := t.TempDir()
	for _, dir := range []string{".svn", ".filees"} {
		if err := os.Mkdir(filepath.Join(wc, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"managed.txt", "private.txt", ".svn/internal", ".filees/internal"} {
		if err := writeFile(filepath.Join(wc, rel), 0444); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := writeFile(outside, 0444); err != nil {
		t.Fatal(err)
	}
	// Windows runners without symlink privilege still exercise metadata protection.
	if err := os.Symlink(outside, filepath.Join(wc, "link.txt")); err != nil {
		t.Logf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(wc, "linked-dir")); err != nil {
		t.Logf("directory symlink unavailable: %v", err)
	}
	if err := m.AutoUnlockOwned(t.Context(), wc, "owner"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, filepath.Join(wc, "managed.txt"), true)
	for _, rel := range []string{"private.txt", ".svn/internal", ".filees/internal"} {
		assertWritable(t, filepath.Join(wc, rel), false)
	}
	assertWritable(t, outside, false)
	if b.seq != 0 || b.unlocks != 0 {
		t.Fatal("local RW mutated server locks")
	}
}

func TestAutoUnlockOwnedFailsClosedOnObservationFailure(t *testing.T) {
	for _, kind := range []string{"properties", "lock", "cancelled", "missing-root"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now()
			b := newFakeBackend()
			b.needsLock = map[string]bool{"doc.txt": true}
			m := openTestManager(t, b, &now, Config{})
			wc := t.TempDir()
			doc := filepath.Join(wc, "doc.txt")
			if err := writeFile(doc, 0444); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := errPartitioned
			root := wc
			switch kind {
			case "properties":
				b.needsLockErr = want
			case "lock":
				b.partitioned = true
			case "cancelled":
				cancel()
				want = context.Canceled
			case "missing-root":
				root = filepath.Join(wc, "absent")
				want = os.ErrNotExist
			}
			if err := m.AutoUnlockOwned(ctx, root, "owner"); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			assertWritable(t, doc, false)
			if b.seq != 0 || b.unlocks != 0 {
				t.Fatal("failed observation mutated locks")
			}
		})
	}
}
