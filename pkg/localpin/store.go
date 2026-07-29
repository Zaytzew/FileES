// Package localpin manages a single local PIN used to gate the desktop
// GUI's optional startup lock and the mandatory entry point into the
// mobile-pairing QR generator. The PIN itself is never stored in
// recoverable form off this exact machine/account - see devicekey.go and
// encrypt.go for why an AES-GCM key derived from local machine/account
// identity was chosen over a password hash like bcrypt.
package localpin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/durable"
)

const Schema = "filees.local-pin/v1"

// DefaultAttempts bounds how many wrong PIN entries are tolerated before
// the store locks permanently (until Setup is called again) - mirrors
// pkg/onboarding's OTP attempt-exhaustion pattern (permanent, not
// time-decayed) rather than inventing a new lockout shape.
const DefaultAttempts = 5

func DefaultRoot() string {
	if root := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); filepath.IsAbs(root) {
		return filepath.Join(filepath.Clean(root), "filees", "localpin")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "filees", "localpin")
}

type record struct {
	Schema          string `json:"schema"`
	EncryptedPIN    string `json:"encrypted_pin"`
	AttemptsLeft    int    `json:"attempts_left"`
	RequireOnLaunch bool   `json:"require_on_launch"`
}

// Store manages the single local PIN record under root (a dedicated 0700
// directory; the record file itself is 0600).
type Store struct {
	path string
}

// Open returns a Store rooted at root (use DefaultRoot() in production;
// tests inject a temp directory).
func Open(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("localpin root must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	// MkdirAll only sets the mode on directories it actually creates - if
	// root already existed (e.g. under a looser umask, or pre-dating this
	// feature), force it to 0700 explicitly rather than trusting whatever
	// mode it already had.
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(root, "pin.json")}, nil
}

func (s *Store) load() (record, bool, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return record{}, false, err
	}
	if rec.Schema != Schema {
		return record{}, false, errors.New("unexpected local PIN schema")
	}
	return rec, true, nil
}

func (s *Store) save(rec record) error {
	rec.Schema = Schema
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, raw, 0o600)
}

// IsConfigured reports whether a PIN has ever been set up.
func (s *Store) IsConfigured() (bool, error) {
	_, ok, err := s.load()
	return ok, err
}

// Setup (re)configures the PIN, resetting any prior lockout. Overwrites
// any existing PIN outright - there is no separate "change PIN" flow.
func (s *Store) Setup(pin []byte) error {
	if len(pin) == 0 {
		return errors.New("PIN must not be empty")
	}
	encrypted, err := encryptPIN(s.path, pin)
	if err != nil {
		return err
	}
	existing, _, _ := s.load()
	return s.save(record{EncryptedPIN: encrypted, AttemptsLeft: DefaultAttempts, RequireOnLaunch: existing.RequireOnLaunch})
}

// Verify checks pin against the stored PIN. locked is true once the
// attempt budget is exhausted (permanently, until Setup is called again) -
// in that state ok is always false regardless of pin, including the
// correct one, so a caller cannot use "does the real PIN still work" to
// probe whether lockout occurred.
func (s *Store) Verify(pin []byte) (ok, locked bool, err error) {
	rec, configured, err := s.load()
	if err != nil {
		return false, false, err
	}
	if !configured {
		return false, false, errors.New("no local PIN configured")
	}
	if rec.AttemptsLeft <= 0 {
		return false, true, nil
	}
	stored, err := decryptPIN(s.path, rec.EncryptedPIN)
	if err != nil {
		return false, false, err
	}
	defer clear(stored)
	match := len(pin) > 0 && subtle.ConstantTimeCompare(stored, pin) == 1
	if match {
		rec.AttemptsLeft = DefaultAttempts
		return true, false, s.save(rec)
	}
	rec.AttemptsLeft--
	if err := s.save(rec); err != nil {
		return false, false, err
	}
	return false, rec.AttemptsLeft <= 0, nil
}

// Clear removes the PIN entirely (e.g. before a future OTP-based reset
// flow lands - not built yet, deliberately deferred).
func (s *Store) Clear() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) RequireOnLaunch() (bool, error) {
	rec, _, err := s.load()
	return rec.RequireOnLaunch, err
}

func (s *Store) SetRequireOnLaunch(require bool) error {
	rec, configured, err := s.load()
	if err != nil {
		return err
	}
	if !configured {
		return errors.New("no local PIN configured")
	}
	rec.RequireOnLaunch = require
	return s.save(rec)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}
