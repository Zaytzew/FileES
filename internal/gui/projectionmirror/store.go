// Package projectionmirror persists the desktop client's last accepted
// per-server state emission. It deliberately treats the emission payload as
// opaque JSON: generation/as_of/stale belong to the remote contract, not to a
// competing client-side definition.
package projectionmirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filees/internal/durable"
	"filees/pkg/privatefile"
)

const Schema = "filees.client-projection-mirror/v1"

// Entry is a client-owned wrapper around one opaque remote emission. The
// server contract remains wholly inside Payload; ReceivedAt records only when
// this client durably accepted that emission.
type Entry struct {
	Schema     string          `json:"schema"`
	ServerID   string          `json:"server_id"`
	ReceivedAt time.Time       `json:"received_at"`
	Payload    json.RawMessage `json:"payload"`
}

// Store owns a private directory containing one atomically replaced file per
// full ServerID. A Store serializes writers; FileES already enforces a single
// desktop GUI instance, so no cross-process lock is needed here.
type Store struct {
	root string
	mu   sync.Mutex
}

func Open(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("projection mirror root must be absolute")
	}
	root = filepath.Clean(root)
	if err := privatefile.EnsureDir(root); err != nil {
		return nil, fmt.Errorf("prepare projection mirror: %w", err)
	}
	return &Store{root: root}, nil
}

// Save atomically replaces the mirror for serverID. Payload must already be a
// complete JSON value produced from a successful remote emission.
func (s *Store) Save(serverID string, receivedAt time.Time, payload []byte) error {
	if err := validateServerID(serverID); err != nil {
		return err
	}
	if receivedAt.IsZero() {
		return errors.New("projection mirror received_at must not be zero")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return fmt.Errorf("projection mirror payload is not valid JSON: %w", err)
	}
	entry := Entry{
		Schema:     Schema,
		ServerID:   serverID,
		ReceivedAt: receivedAt.UTC(),
		Payload:    append(json.RawMessage(nil), compact.Bytes()...),
	}
	raw, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode projection mirror: %w", err)
	}
	raw = append(raw, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(s.root, s.path(serverID), raw)
}

// Load returns the last complete mirror for serverID. A corrupt mirror is an
// error for that server only; callers can continue loading other activations.
func (s *Store) Load(serverID string) (Entry, bool, error) {
	if err := validateServerID(serverID); err != nil {
		return Entry{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(serverID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("inspect projection mirror: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Entry{}, false, errors.New("projection mirror is not a regular file")
	}
	if err := privatefile.Verify(path); err != nil {
		return Entry{}, false, fmt.Errorf("verify projection mirror: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, false, fmt.Errorf("read projection mirror: %w", err)
	}
	entry, err := decode(raw)
	if err != nil {
		return Entry{}, false, err
	}
	if entry.ServerID != serverID {
		return Entry{}, false, fmt.Errorf("projection mirror identity mismatch: got %q, want %q", entry.ServerID, serverID)
	}
	return entry, true, nil
}

// Prune removes published mirrors not belonging to activeServerIDs, plus
// abandoned temporary files left by a killed writer. Unknown files are left
// untouched; this is cleanup of this store's namespace, not a recursive wipe.
func (s *Store) Prune(activeServerIDs []string) (int, error) {
	keep := make(map[string]struct{}, len(activeServerIDs))
	for _, serverID := range activeServerIDs {
		if err := validateServerID(serverID); err != nil {
			return 0, err
		}
		keep[fileName(serverID)] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, fmt.Errorf("list projection mirrors: %w", err)
	}
	removed := 0
	var removeErrors []error
	for _, item := range entries {
		name := item.Name()
		_, active := keep[name]
		published := !item.IsDir() && isMirrorName(name)
		temporary := !item.IsDir() && strings.HasPrefix(name, ".mirror-") && strings.HasSuffix(name, ".tmp")
		if (published && !active) || temporary {
			if err := os.Remove(filepath.Join(s.root, name)); err != nil {
				removeErrors = append(removeErrors, fmt.Errorf("remove projection mirror %s: %w", name, err))
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		if err := durable.SyncDirectory(s.root); err != nil {
			removeErrors = append(removeErrors, fmt.Errorf("sync projection mirror directory: %w", err))
		}
	}
	return removed, errors.Join(removeErrors...)
}

func (s *Store) path(serverID string) string {
	return filepath.Join(s.root, fileName(serverID))
}

func fileName(serverID string) string {
	digest := sha256.Sum256([]byte(serverID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func isMirrorName(name string) bool {
	const digestLength = sha256.Size * 2
	if len(name) != digestLength+len(".json") || !strings.HasSuffix(name, ".json") {
		return false
	}
	digest, err := hex.DecodeString(name[:digestLength])
	return err == nil && len(digest) == sha256.Size
}

func validateServerID(serverID string) error {
	if serverID == "" || strings.TrimSpace(serverID) != serverID {
		return errors.New("projection mirror ServerID must be non-empty and canonical")
	}
	return nil
}

func decode(raw []byte) (Entry, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var entry Entry
	if err := decoder.Decode(&entry); err != nil {
		return Entry{}, fmt.Errorf("decode projection mirror: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Entry{}, errors.New("projection mirror contains trailing data")
	}
	if entry.Schema != Schema {
		return Entry{}, fmt.Errorf("unsupported projection mirror schema %q", entry.Schema)
	}
	if err := validateServerID(entry.ServerID); err != nil {
		return Entry{}, err
	}
	if entry.ReceivedAt.IsZero() {
		return Entry{}, errors.New("projection mirror received_at is missing")
	}
	if !json.Valid(entry.Payload) {
		return Entry{}, errors.New("projection mirror payload is invalid")
	}
	return entry, nil
}

func writeAtomic(root, path string, raw []byte) error {
	temporary, err := os.CreateTemp(root, ".mirror-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := privatefile.Harden(temporaryPath); err != nil {
		return err
	}
	var renameErr error
	for attempt := 0; attempt < 20; attempt++ {
		if renameErr = os.Rename(temporaryPath, path); renameErr == nil {
			return durable.SyncDirectory(root)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return renameErr
}
