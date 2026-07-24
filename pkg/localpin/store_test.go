package localpin

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestIsConfiguredReflectsSetup(t *testing.T) {
	store := newTestStore(t)
	if configured, err := store.IsConfigured(); err != nil || configured {
		t.Fatalf("configured=%v err=%v, want false before Setup", configured, err)
	}
	if err := store.Setup([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if configured, err := store.IsConfigured(); err != nil || !configured {
		t.Fatalf("configured=%v err=%v, want true after Setup", configured, err)
	}
}

func TestVerifyRoundTripsCorrectAndRejectsWrongPIN(t *testing.T) {
	store := newTestStore(t)
	if err := store.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	if ok, locked, err := store.Verify([]byte("4242")); err != nil || !ok || locked {
		t.Fatalf("correct PIN: ok=%v locked=%v err=%v", ok, locked, err)
	}
	if ok, locked, err := store.Verify([]byte("0000")); err != nil || ok || locked {
		t.Fatalf("wrong PIN: ok=%v locked=%v err=%v", ok, locked, err)
	}
}

func TestVerifyLocksAfterAttemptsExhaustedAndRejectsEvenCorrectPIN(t *testing.T) {
	store := newTestStore(t)
	if err := store.Setup([]byte("9999")); err != nil {
		t.Fatal(err)
	}
	var lastLocked bool
	for i := 0; i < DefaultAttempts; i++ {
		ok, locked, err := store.Verify([]byte("wrong"))
		if err != nil || ok {
			t.Fatalf("attempt %d: ok=%v err=%v", i, ok, err)
		}
		lastLocked = locked
	}
	if !lastLocked {
		t.Fatal("store did not report locked after exhausting all attempts")
	}
	// Even the genuinely correct PIN must now be rejected - lockout is not
	// bypassable by "getting lucky" on a later guess.
	if ok, locked, err := store.Verify([]byte("9999")); err != nil || ok || !locked {
		t.Fatalf("post-lockout correct PIN: ok=%v locked=%v err=%v", ok, locked, err)
	}
}

func TestSetupResetsLockout(t *testing.T) {
	store := newTestStore(t)
	if err := store.Setup([]byte("1111")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < DefaultAttempts; i++ {
		store.Verify([]byte("wrong"))
	}
	if _, locked, _ := store.Verify([]byte("1111")); !locked {
		t.Fatal("expected locked before re-Setup")
	}
	if err := store.Setup([]byte("2222")); err != nil {
		t.Fatal(err)
	}
	if ok, locked, err := store.Verify([]byte("2222")); err != nil || !ok || locked {
		t.Fatalf("after re-Setup: ok=%v locked=%v err=%v", ok, locked, err)
	}
}

func TestRequireOnLaunchDefaultsFalseAndPersists(t *testing.T) {
	store := newTestStore(t)
	if err := store.Setup([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if require, err := store.RequireOnLaunch(); err != nil || require {
		t.Fatalf("require=%v err=%v, want false by default", require, err)
	}
	if err := store.SetRequireOnLaunch(true); err != nil {
		t.Fatal(err)
	}
	if require, err := store.RequireOnLaunch(); err != nil || !require {
		t.Fatalf("require=%v err=%v, want true after SetRequireOnLaunch", require, err)
	}
	// A fresh Store instance over the same root must see the persisted value.
	reopened, err := Open(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if require, err := reopened.RequireOnLaunch(); err != nil || !require {
		t.Fatalf("reopened require=%v err=%v, want true", require, err)
	}
}

func TestClearRemovesPIN(t *testing.T) {
	store := newTestStore(t)
	if err := store.Setup([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if configured, err := store.IsConfigured(); err != nil || configured {
		t.Fatalf("configured=%v err=%v, want false after Clear", configured, err)
	}
	// Clear on an already-cleared store is not an error.
	if err := store.Clear(); err != nil {
		t.Fatalf("second Clear returned error: %v", err)
	}
}

func TestStoredPINFilePermissions(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("localpin root mode=%v, want 0700", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(filepath.Join(root, "pin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("pin.json mode=%v, want 0600", fileInfo.Mode().Perm())
	}
}
