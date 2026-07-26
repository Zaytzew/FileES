//go:build !windows

package activation

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const (
	sessionLeaseSchema = "filees.session-lease/v1"
	sessionRecordName  = "session.json"
	sessionFIFOName    = "revoke.fifo"
)

// SessionMetadata contains only the identity needed to match a revoke. It
// intentionally contains no PID: stale lease cleanup must never target a PID
// that may have been reused by an unrelated process.
type SessionMetadata struct {
	Schema      string    `json:"schema"`
	SessionID   string    `json:"session_id"`
	OperationID string    `json:"operation_id"`
	ClientID    string    `json:"client_id"`
	RealmID     string    `json:"realm_id"`
	StartedAt   time.Time `json:"started_at"`
}

// SessionLease is owned by exactly one forced-command supervisor. The read
// side is opened O_RDWR|O_NONBLOCK before the directory is exposed, so a
// revoker can only write to a live lease and never blocks behind a vanished
// reader.
type SessionLease struct {
	Metadata SessionMetadata
	Dir      string
	fifo     *os.File
}

func createSessionLease(root string, metadata SessionMetadata) (*SessionLease, error) {
	if err := validateSessionRoot(root); err != nil {
		return nil, err
	}
	if metadata.OperationID == "" || metadata.ClientID == "" || metadata.RealmID == "" || metadata.StartedAt.IsZero() {
		return nil, errors.New("session lease metadata is incomplete")
	}
	metadata.Schema = sessionLeaseSchema
	metadata.StartedAt = metadata.StartedAt.UTC()
	var dir string
	for attempt := 0; attempt < 4; attempt++ {
		id, err := newSessionID()
		if err != nil {
			return nil, err
		}
		metadata.SessionID = id
		dir = filepath.Join(root, sessionLeaseDirectoryName(id))
		if err := os.Mkdir(dir, 0o700); err == nil {
			break
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create session lease: %w", err)
		}
		dir = ""
	}
	if dir == "" {
		return nil, errors.New("create session lease: repeated random identifier collision")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("protect session lease: %w", err)
	}
	cleanup := func(err error) (*SessionLease, error) {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := atomicWriteJSON(filepath.Join(dir, sessionRecordName), metadata, 0o600); err != nil {
		return cleanup(fmt.Errorf("write session lease: %w", err))
	}
	fifoPath := filepath.Join(dir, sessionFIFOName)
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		return cleanup(fmt.Errorf("create session revoke fifo: %w", err))
	}
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return cleanup(fmt.Errorf("open session revoke fifo: %w", err))
	}
	return &SessionLease{Metadata: metadata, Dir: dir, fifo: fifo}, nil
}

func (lease *SessionLease) Revoked() (bool, error) {
	if lease == nil || lease.fifo == nil {
		return false, errors.New("session lease is closed")
	}
	var marker [16]byte
	// os.File.Read registers non-blocking descriptors with Go's netpoller and
	// waits for readiness instead of surfacing EAGAIN. This FIFO is a periodic
	// poll source, so use unix.Read to preserve O_NONBLOCK semantics.
	n, err := unix.Read(int(lease.fifo.Fd()), marker[:])
	if n > 0 {
		return true, nil
	}
	if err == nil || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return false, fmt.Errorf("read session revoke fifo: %w", err)
}

func (lease *SessionLease) Close() error {
	if lease == nil {
		return nil
	}
	var closeErr error
	if lease.fifo != nil {
		closeErr = lease.fifo.Close()
		lease.fifo = nil
	}
	if err := removeSessionLease(lease.Dir); err != nil && closeErr == nil {
		closeErr = err
	}
	return closeErr
}

func signalSessionLeases(root, clientID, realmID string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list session leases: %w", err)
	}
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "session-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		metadata, err := readSessionMetadata(dir)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if (clientID != "" && metadata.ClientID != clientID) || (realmID != "" && metadata.RealmID != realmID) {
			continue
		}
		fifoPath := filepath.Join(dir, sessionFIFOName)
		fifoInfo, err := os.Lstat(fifoPath)
		if err != nil || !privateSessionFIFO(fifoInfo) {
			if firstErr == nil {
				firstErr = errors.New("session revoke fifo is unsafe")
			}
			continue
		}
		fifo, err := os.OpenFile(fifoPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if errors.Is(err, syscall.ENXIO) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("open session revoke fifo: %w", err)
			}
			continue
		}
		// Preserve O_NONBLOCK semantics here as well: a full or otherwise
		// unwritable FIFO must fail the revoke notification rather than park
		// the administrative caller in Go's netpoller.
		_, writeErr := unix.Write(int(fifo.Fd()), []byte{'R'})
		closeErr := fifo.Close()
		if writeErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("write session revoke fifo: %w", writeErr)
		}
		if closeErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("close session revoke fifo: %w", closeErr)
		}
	}
	return firstErr
}

func readSessionMetadata(dir string) (SessionMetadata, error) {
	info, err := os.Lstat(dir)
	if err != nil || !privateSessionDirectory(info) {
		return SessionMetadata{}, errors.New("session lease directory is unsafe")
	}
	metadataPath := filepath.Join(dir, sessionRecordName)
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil || !privateSessionFile(metadataInfo) {
		return SessionMetadata{}, errors.New("session lease metadata is unsafe")
	}
	raw, err := os.ReadFile(metadataPath)
	if err != nil || len(raw) > 16*1024 {
		return SessionMetadata{}, errors.New("session lease metadata cannot be read")
	}
	var metadata SessionMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata.Schema != sessionLeaseSchema || !validSessionID(metadata.SessionID) || filepath.Base(dir) != sessionLeaseDirectoryName(metadata.SessionID) || metadata.StartedAt.IsZero() {
		return SessionMetadata{}, errors.New("session lease metadata is invalid")
	}
	for label, id := range map[string]string{"operation_id": metadata.OperationID, "client_id": metadata.ClientID, "realm_id": metadata.RealmID} {
		if _, err := uuid.Parse(id); err != nil {
			return SessionMetadata{}, fmt.Errorf("session lease %s is invalid", label)
		}
	}
	return metadata, nil
}

func validateSessionRoot(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("session root must be absolute")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return fmt.Errorf("create session root: %w", err)
		}
		info, err = os.Lstat(root)
	}
	if err != nil || !privateSessionDirectory(info) {
		return errors.New("session root must be a private directory")
	}
	return nil
}

func removeSessionLease(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !privateSessionDirectory(info) {
		return errors.New("refusing to remove unsafe session lease")
	}
	sessionID := strings.TrimPrefix(filepath.Base(dir), "session-")
	if !validSessionID(sessionID) || filepath.Base(dir) != sessionLeaseDirectoryName(sessionID) {
		return errors.New("refusing to remove invalid session lease path")
	}

	type leaseArtifact struct {
		name string
		safe func(os.FileInfo) bool
	}
	artifacts := []leaseArtifact{
		{name: sessionFIFOName, safe: privateSessionFIFO},
		{name: sessionRecordName, safe: privateSessionFile},
	}
	present := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		path := filepath.Join(dir, artifact.name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !artifact.safe(info) {
			return fmt.Errorf("refusing to remove unsafe session lease artifact %s", artifact.name)
		}
		present = append(present, path)
	}
	for _, path := range present {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove session lease artifact %s: %w", filepath.Base(path), err)
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove session lease directory: %w", err)
	}
	return nil
}

func newSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func sessionLeaseDirectoryName(sessionID string) string {
	return "session-" + sessionID
}

func validSessionID(sessionID string) bool {
	if len(sessionID) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(sessionID)
	return err == nil && len(decoded) == 16
}

func privateSessionDirectory(info os.FileInfo) bool {
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func privateSessionFile(info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func privateSessionFIFO(info os.FileInfo) bool {
	if info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
