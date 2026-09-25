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

// Owner's production, 2026-09-25: a drawing held by the owner's own passport
// went read-only after every one of his commits (view unknown until the
// server's projection reached the new revision) and after every heartbeat
// (view still carrying the previous token), and BricsCAD refused to save it.
func TestConfirmedPassportKeepsWriteAccessWhileTheViewLags(t *testing.T) {
	now := time.Now()
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	b.needsLock = map[string]bool{"held.dwg": true, "speculative.dwg": true}
	view := OwnershipView{Owners: map[string]string{"held.dwg": "guest", "speculative.dwg": "a"}, Holds: map[string]OwnershipHold{}}
	var unavailable error
	m := openTestManager(t, b, &now, Config{WorkingCopy: wc, Ownership: func(context.Context) (OwnershipView, error) { return view, unavailable }})
	held, speculative := filepath.Join(wc, "held.dwg"), filepath.Join(wc, "speculative.dwg")
	for _, p := range []string{held, speculative} {
		if err := writeFile(p, 0444); err != nil {
			t.Fatal(err)
		}
	}
	passports, _, err := m.Acquire(t.Context(), []string{held}, "a")
	if err != nil {
		t.Fatal(err)
	}
	view.Holds["held.dwg"] = OwnershipHold{Token: passports[0].FencingToken, RealmID: "a"}
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, held, true)
	assertWritable(t, speculative, true)

	unavailable = errors.New("projection behind the new revision")
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); !errors.Is(err, unavailable) {
		t.Fatal(err)
	}
	assertWritable(t, held, true)         // confirmed passport: kept
	assertWritable(t, speculative, false) // speculative access: still revoked

	unavailable = nil
	view.Holds["held.dwg"] = OwnershipHold{Token: "previous-token", RealmID: "a"} // heartbeat rotated it
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, held, true)

	view.Holds["held.dwg"] = OwnershipHold{Token: "their-token", RealmID: "b"}
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, held, false) // another realm holds it: revoked

	// In the middle of a renewal nothing changes either way.
	if err := os.Chmod(held, 0644); err != nil {
		t.Fatal(err)
	}
	renewing := m.passports[held]
	renewing.State, renewing.Pending = StatePending, &LockIntent{Mode: "renew", Path: held}
	m.passports[held] = renewing
	if err := m.AutoUnlockOwned(t.Context(), wc, "a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, held, true)
	if b.forceCalls != 0 || b.unlocks != 0 {
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
