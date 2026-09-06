package passport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPerPathAccessReconcilesOwnershipAndCanonicalHolds(t *testing.T) {
	now := time.Now()
	wc := t.TempDir()
	b := newFakeBackend()
	b.needsLock = map[string]bool{"owner": true, "guest": true, "foreign": true, "same": true, "unknown": true}
	view := OwnershipView{Owners: map[string]string{"owner": "a", "guest": "b", "foreign": "a", "same": "a"}, Holds: map[string]OwnershipHold{"foreign": {Token: "f", RealmID: "b"}, "same": {Token: "s", RealmID: "a"}}}
	var unavailable error
	m := openTestManager(t, b, &now, Config{WorkingCopy: wc, Ownership: func(context.Context) (OwnershipView, error) { return view, unavailable }})
	for p := range b.needsLock {
		if err := writeFile(filepath.Join(wc, p), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFile(filepath.Join(wc, "unmanaged"), 0644); err != nil {
		t.Fatal(err)
	}
	check := func(p string, want bool) {
		t.Helper()
		info, err := os.Stat(filepath.Join(wc, p))
		if err != nil {
			t.Fatal(err)
		}
		if (info.Mode().Perm()&0200 != 0) != want {
			t.Fatalf("%s: mode %o", p, info.Mode().Perm())
		}
	}
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); err != nil {
		t.Fatal(err)
	}
	check("owner", true)
	check("guest", false)
	check("foreign", false)
	check("same", true)
	check("unknown", false)
	check("unmanaged", true)
	view.Owners["owner"] = "b" // revoke, no new data revision
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); err != nil {
		t.Fatal(err)
	}
	check("owner", false)
	unavailable = errors.New("broker unavailable")
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); !errors.Is(err, unavailable) {
		t.Fatal(err)
	}
	check("same", false)
	check("unmanaged", true)
	if b.seq != 0 || b.forceCalls != 0 || b.unlocks != 0 {
		t.Fatal("local access mutated server locks")
	}
}

func TestAcquireOwnedDoesNotBorrowGuestObjects(t *testing.T) {
	now := time.Now()
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	view := OwnershipView{Owners: map[string]string{"a": "owner", "b": "guest"}}
	m := openTestManager(t, b, &now, Config{WorkingCopy: wc, Ownership: func(context.Context) (OwnershipView, error) { return view, nil }})
	a, bpath := filepath.Join(wc, "a"), filepath.Join(wc, "b")
	if err := m.AcquireOwned(t.Context(), []string{bpath}, "owner"); !errors.Is(err, ErrNoPassport) {
		t.Fatalf("implicit borrow: %v", err)
	}
	if b.seq != 0 {
		t.Fatal("borrowed foreign file")
	}
	if err := m.AcquireOwned(t.Context(), []string{a}, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Acquire(t.Context(), []string{bpath}, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AcquireOwned(t.Context(), []string{a, bpath}, "owner"); err != nil {
		t.Fatal(err)
	}
	if b.seq != 2 || b.forceCalls != 0 {
		t.Fatalf("locks=%d force=%d", b.seq, b.forceCalls)
	}
}
