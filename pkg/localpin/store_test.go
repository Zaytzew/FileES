package localpin

import (
	"encoding/base64"
	"os"
	"os/user"
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

// TestVerifySurvivesMACAddressChangeAfterSetup is the core regression test
// for the "Stabilność lokalnego PIN-u urządzenia" gap: a PIN set up under
// one MAC address must still verify after the machine reports a different
// (or no) "first active" interface, since encryption now binds to a
// persisted device_id rather than live network hardware.
func TestVerifySurvivesMACAddressChangeAfterSetup(t *testing.T) {
	original := firstMACAddress
	defer func() { firstMACAddress = original }()

	firstMACAddress = func() string { return "aa:bb:cc:dd:ee:ff" }
	store := newTestStore(t)
	if err := store.Setup([]byte("1234")); err != nil {
		t.Fatal(err)
	}

	for _, mac := range []string{"11:22:33:44:55:66", ""} {
		firstMACAddress = func() string { return mac }
		if ok, locked, err := store.Verify([]byte("1234")); err != nil || !ok || locked {
			t.Fatalf("mac=%q: verify after MAC change: ok=%v locked=%v err=%v", mac, ok, locked, err)
		}
	}
}

// TestVerifyMigratesLegacyMACOnlyEncryptedRecordTransparently simulates an
// installation whose pin.json predates deviceInstanceID: the record is
// encrypted only under the old MAC-derived key. The first successful
// Verify must both accept the PIN and silently re-encrypt the record under
// the current (device_id-based) primary key, so a later MAC change no
// longer matters.
func TestVerifyMigratesLegacyMACOnlyEncryptedRecordTransparently(t *testing.T) {
	original := firstMACAddress
	defer func() { firstMACAddress = original }()
	firstMACAddress = func() string { return "aa:bb:cc:dd:ee:ff" }

	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	legacyEncrypted := legacyEncryptForTest(t, store.path, []byte("4321"))
	if err := store.save(record{EncryptedPIN: legacyEncrypted, AttemptsLeft: DefaultAttempts}); err != nil {
		t.Fatal(err)
	}
	before, _, err := store.load()
	if err != nil {
		t.Fatal(err)
	}

	if ok, locked, err := store.Verify([]byte("4321")); err != nil || !ok || locked {
		t.Fatalf("verify legacy record: ok=%v locked=%v err=%v", ok, locked, err)
	}

	after, _, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if after.EncryptedPIN == before.EncryptedPIN {
		t.Fatal("legacy record was not re-encrypted under the primary key on first successful verify")
	}

	// The migrated record must no longer depend on the (now-gone) MAC.
	firstMACAddress = func() string { return "" }
	if ok, locked, err := store.Verify([]byte("4321")); err != nil || !ok || locked {
		t.Fatalf("verify migrated record after MAC removal: ok=%v locked=%v err=%v", ok, locked, err)
	}
}

// legacyEncryptForTest reproduces exactly what the pre-deviceInstanceID
// encryptPIN did: encrypt under the sole MAC-derived key, bypassing
// deviceKeys' current device_id-first ordering entirely.
func legacyEncryptForTest(t *testing.T, path string, pin []byte) string {
	t.Helper()
	hostname, _ := os.Hostname()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	key := deriveKey(hostname, "uid:"+current.Uid, path, firstMACAddress())
	gcm, err := newGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	ciphertext := gcm.Seal(nonce, nonce, pin, nil)
	return base64.StdEncoding.EncodeToString(ciphertext)
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
