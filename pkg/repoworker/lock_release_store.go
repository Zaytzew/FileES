package repoworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	lockReleaseRequestSchema     = "filees.lock-release-request/v1"
	DefaultLockReleaseRequestTTL = 3 * time.Hour
)

type LockReleaseState string

const (
	LockReleasePending   LockReleaseState = "pending"
	LockReleaseDismissed LockReleaseState = "dismissed"
	LockReleaseAccepted  LockReleaseState = "accepted"
	LockReleaseLockGone  LockReleaseState = "lock_gone"
	LockReleaseExpired   LockReleaseState = "expired"
	LockReleaseStale     LockReleaseState = "stale"
)

var (
	ErrLockReleaseNotFound     = errors.New("lock release request not found")
	ErrLockReleaseForbidden    = errors.New("lock release request actor is not the holder")
	ErrLockReleaseTerminal     = errors.New("lock release request is terminal")
	ErrLockReleaseInvalidState = errors.New("invalid lock release request transition")
)

// LockReleaseRecord is the server-owned state projected to both sides of one
// request. ObservedLockID is the opaque SVN lock token; the holder identity is
// derived from the authoritative lock and is never accepted from a client
// payload.
type LockReleaseRecord struct {
	Schema            string           `json:"schema"`
	RequestID         string           `json:"request_id"`
	RepoID            string           `json:"repo_id"`
	Path              string           `json:"path"`
	ObservedLockID    string           `json:"observed_lock_id"`
	RequesterClientID string           `json:"requester_client_id"`
	RequesterRealmID  string           `json:"requester_realm_id"`
	HolderClientID    string           `json:"holder_client_id"`
	HolderRealmID     string           `json:"holder_realm_id,omitempty"`
	State             LockReleaseState `json:"state"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	ExpiresAt         time.Time        `json:"expires_at"`
}

// LockReleaseRequest contains only identities already resolved by the worker.
// The forced-command handler must derive the requester from its session and
// the holder from the current SVN lock before calling Request.
type LockReleaseRequest struct {
	RepoID            string
	Path              string
	ObservedLockID    string
	RequesterClientID string
	RequesterRealmID  string
	HolderClientID    string
	HolderRealmID     string
}

// LockReleaseObservation is a fresh, authoritative observation of the lock
// targeted by a pending request. A nil observation means the lock disappeared;
// a different token makes the request stale. The same token may move to a new
// holder during a same-realm passport migration.
type LockReleaseObservation struct {
	ObservedLockID string
	HolderClientID string
	HolderRealmID  string
}

// FileLockReleaseStore keeps one durable record per
// (repo, observed_lock_id, requester_client_id). The deterministic filename is
// a digest, so neither a repository path nor an SVN token reaches filesystem
// path construction.
type FileLockReleaseStore struct {
	Root string
	TTL  time.Duration
	Now  func() time.Time
	mu   sync.Mutex
}

func (s *FileLockReleaseStore) Request(input LockReleaseRequest) (LockReleaseRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(); err != nil {
		return LockReleaseRecord{}, false, err
	}
	if err := validateLockReleaseRequest(input); err != nil {
		return LockReleaseRecord{}, false, err
	}
	now := s.now()
	file := s.keyPath(input.RepoID, input.ObservedLockID, input.RequesterClientID)
	if current, err := s.loadPathLocked(file); err == nil {
		if current.RepoID != input.RepoID || current.ObservedLockID != input.ObservedLockID || current.RequesterClientID != input.RequesterClientID {
			return LockReleaseRecord{}, false, errors.New("lock release request key conflicts with stored record")
		}
		if current.State == LockReleaseAccepted || current.State == LockReleaseLockGone || current.State == LockReleaseStale || now.Before(current.ExpiresAt) {
			return current, false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return LockReleaseRecord{}, false, err
	}
	record := LockReleaseRecord{
		Schema: lockReleaseRequestSchema, RequestID: uuid.NewString(),
		RepoID: input.RepoID, Path: input.Path, ObservedLockID: input.ObservedLockID,
		RequesterClientID: input.RequesterClientID, RequesterRealmID: input.RequesterRealmID,
		HolderClientID: input.HolderClientID, HolderRealmID: input.HolderRealmID,
		State: LockReleasePending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(s.ttl()),
	}
	if err := s.savePathLocked(file, record); err != nil {
		return LockReleaseRecord{}, false, err
	}
	return record, true, nil
}

func (s *FileLockReleaseStore) Get(requestID string) (LockReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(); err != nil {
		return LockReleaseRecord{}, err
	}
	record, file, err := s.findLocked(requestID)
	if err != nil {
		return LockReleaseRecord{}, err
	}
	return s.expirePendingLocked(file, record)
}

// ListForClient returns both sides of the same record: requests made by the
// client and requests currently addressed to a lock held by the client.
func (s *FileLockReleaseStore) ListForClient(clientID string) ([]LockReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(); err != nil {
		return nil, err
	}
	if err := validateLockReleaseUUID("client ID", clientID); err != nil {
		return nil, err
	}
	files, err := s.recordPathsLocked()
	if err != nil {
		return nil, err
	}
	records := make([]LockReleaseRecord, 0)
	for _, file := range files {
		record, err := s.loadPathLocked(file)
		if err != nil {
			return nil, err
		}
		record, err = s.expirePendingLocked(file, record)
		if err != nil {
			return nil, err
		}
		if record.RequesterClientID == clientID || record.HolderClientID == clientID {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].RequestID < records[j].RequestID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

// Respond records the holder's explicit decision. Only pending records can be
// accepted or dismissed, and only the holder derived from the lock may act.
func (s *FileLockReleaseStore) Respond(requestID, holderClientID string, next LockReleaseState) (LockReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(); err != nil {
		return LockReleaseRecord{}, err
	}
	if err := validateLockReleaseUUID("holder client ID", holderClientID); err != nil {
		return LockReleaseRecord{}, err
	}
	if next != LockReleaseAccepted && next != LockReleaseDismissed {
		return LockReleaseRecord{}, ErrLockReleaseInvalidState
	}
	record, file, err := s.findLocked(requestID)
	if err != nil {
		return LockReleaseRecord{}, err
	}
	record, err = s.expirePendingLocked(file, record)
	if err != nil {
		return LockReleaseRecord{}, err
	}
	if record.HolderClientID != holderClientID {
		return LockReleaseRecord{}, ErrLockReleaseForbidden
	}
	if record.State != LockReleasePending {
		return record, ErrLockReleaseTerminal
	}
	record.State = next
	record.UpdatedAt = s.now()
	if err := s.savePathLocked(file, record); err != nil {
		return LockReleaseRecord{}, err
	}
	return record, nil
}

// Reconcile applies a fresh authoritative lock observation to a pending
// request. A same-token migration retargets the holder; disappearance or token
// replacement is terminal and never implies acceptance.
func (s *FileLockReleaseStore) Reconcile(requestID string, observation *LockReleaseObservation) (LockReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(); err != nil {
		return LockReleaseRecord{}, err
	}
	record, file, err := s.findLocked(requestID)
	if err != nil {
		return LockReleaseRecord{}, err
	}
	record, err = s.expirePendingLocked(file, record)
	if err != nil {
		return LockReleaseRecord{}, err
	}
	if record.State != LockReleasePending {
		return record, ErrLockReleaseTerminal
	}
	now := s.now()
	if observation == nil {
		record.State = LockReleaseLockGone
		record.UpdatedAt = now
	} else {
		if err := validateObservedLockID(observation.ObservedLockID); err != nil {
			return LockReleaseRecord{}, err
		}
		if observation.ObservedLockID != record.ObservedLockID {
			record.State = LockReleaseStale
			record.UpdatedAt = now
		} else {
			if err := validateHolderObservation(*observation, record.RequesterClientID); err != nil {
				return LockReleaseRecord{}, err
			}
			if record.HolderClientID == observation.HolderClientID && record.HolderRealmID == observation.HolderRealmID {
				return record, nil
			}
			record.HolderClientID = observation.HolderClientID
			record.HolderRealmID = observation.HolderRealmID
			record.UpdatedAt = now
		}
	}
	if err := s.savePathLocked(file, record); err != nil {
		return LockReleaseRecord{}, err
	}
	return record, nil
}

func (s *FileLockReleaseStore) ensureLocked() error {
	if !filepath.IsAbs(s.Root) {
		return errors.New("lock release request root must be absolute")
	}
	return os.MkdirAll(filepath.Clean(s.Root), 0o700)
}

func (s *FileLockReleaseStore) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultLockReleaseRequestTTL
}

func (s *FileLockReleaseStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *FileLockReleaseStore) keyPath(repoID, token, requester string) string {
	digest := sha256.Sum256([]byte(repoID + "\x00" + token + "\x00" + requester))
	return filepath.Join(filepath.Clean(s.Root), hex.EncodeToString(digest[:])+".json")
}

func (s *FileLockReleaseStore) recordPathsLocked() ([]string, error) {
	entries, err := os.ReadDir(filepath.Clean(s.Root))
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) != 64+len(".json") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(entry.Name(), ".json")); err != nil {
			continue
		}
		paths = append(paths, filepath.Join(filepath.Clean(s.Root), entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *FileLockReleaseStore) findLocked(requestID string) (LockReleaseRecord, string, error) {
	if err := validateLockReleaseUUID("request ID", requestID); err != nil {
		return LockReleaseRecord{}, "", err
	}
	files, err := s.recordPathsLocked()
	if err != nil {
		return LockReleaseRecord{}, "", err
	}
	var found LockReleaseRecord
	var foundPath string
	for _, file := range files {
		record, err := s.loadPathLocked(file)
		if err != nil {
			return LockReleaseRecord{}, "", err
		}
		if record.RequestID != requestID {
			continue
		}
		if foundPath != "" {
			return LockReleaseRecord{}, "", errors.New("duplicate lock release request ID")
		}
		found, foundPath = record, file
	}
	if foundPath == "" {
		return LockReleaseRecord{}, "", ErrLockReleaseNotFound
	}
	return found, foundPath, nil
}

func (s *FileLockReleaseStore) loadPathLocked(file string) (LockReleaseRecord, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return LockReleaseRecord{}, err
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(raw), 64<<10))
	decoder.DisallowUnknownFields()
	var record LockReleaseRecord
	if err := decoder.Decode(&record); err != nil {
		return LockReleaseRecord{}, fmt.Errorf("decode lock release request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return LockReleaseRecord{}, errors.New("lock release request contains trailing data")
	}
	if err := record.Validate(); err != nil {
		return LockReleaseRecord{}, err
	}
	return record, nil
}

func (s *FileLockReleaseStore) savePathLocked(file string, record LockReleaseRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Clean(s.Root), ".lock-release-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, file); err != nil {
		return err
	}
	return syncDirectory(filepath.Clean(s.Root))
}

func (s *FileLockReleaseStore) expirePendingLocked(file string, record LockReleaseRecord) (LockReleaseRecord, error) {
	if record.State != LockReleasePending || s.now().Before(record.ExpiresAt) {
		return record, nil
	}
	record.State = LockReleaseExpired
	record.UpdatedAt = s.now()
	if err := s.savePathLocked(file, record); err != nil {
		return LockReleaseRecord{}, err
	}
	return record, nil
}

func (r LockReleaseRecord) Validate() error {
	if r.Schema != lockReleaseRequestSchema {
		return errors.New("invalid lock release request schema")
	}
	for field, value := range map[string]string{
		"request ID": r.RequestID, "repository ID": r.RepoID,
		"requester client ID": r.RequesterClientID, "requester realm ID": r.RequesterRealmID,
		"holder client ID": r.HolderClientID,
	} {
		if err := validateLockReleaseUUID(field, value); err != nil {
			return err
		}
	}
	if r.HolderRealmID != "" {
		if err := validateLockReleaseUUID("holder realm ID", r.HolderRealmID); err != nil {
			return err
		}
	}
	if r.RequesterClientID == r.HolderClientID {
		return errors.New("lock release requester cannot be the holder")
	}
	if err := validateLockReleasePath(r.Path); err != nil {
		return err
	}
	if err := validateObservedLockID(r.ObservedLockID); err != nil {
		return err
	}
	if !validLockReleaseState(r.State) {
		return errors.New("invalid lock release request state")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || !r.ExpiresAt.After(r.CreatedAt) {
		return errors.New("invalid lock release request timestamps")
	}
	return nil
}

func validateLockReleaseRequest(input LockReleaseRequest) error {
	for field, value := range map[string]string{
		"repository ID": input.RepoID, "requester client ID": input.RequesterClientID,
		"requester realm ID": input.RequesterRealmID, "holder client ID": input.HolderClientID,
	} {
		if err := validateLockReleaseUUID(field, value); err != nil {
			return err
		}
	}
	if input.HolderRealmID != "" {
		if err := validateLockReleaseUUID("holder realm ID", input.HolderRealmID); err != nil {
			return err
		}
	}
	if input.RequesterClientID == input.HolderClientID {
		return errors.New("lock release requester cannot be the holder")
	}
	if err := validateLockReleasePath(input.Path); err != nil {
		return err
	}
	return validateObservedLockID(input.ObservedLockID)
}

func validateHolderObservation(observation LockReleaseObservation, requesterClientID string) error {
	if err := validateObservedLockID(observation.ObservedLockID); err != nil {
		return err
	}
	if err := validateLockReleaseUUID("holder client ID", observation.HolderClientID); err != nil {
		return err
	}
	if observation.HolderRealmID != "" {
		if err := validateLockReleaseUUID("holder realm ID", observation.HolderRealmID); err != nil {
			return err
		}
	}
	if observation.HolderClientID == requesterClientID {
		return errors.New("lock release requester cannot become the holder")
	}
	return nil
}

func validateLockReleaseUUID(field, value string) error {
	if _, err := uuid.Parse(value); err != nil {
		return fmt.Errorf("lock release %s must be UUID", field)
	}
	return nil
}

func validateLockReleasePath(value string) error {
	if value == "" || len(value) > 4096 || value != strings.TrimSpace(value) || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\x00\r\n") || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return errors.New("lock release path must be canonical and repository-relative")
	}
	return nil
}

func validateObservedLockID(value string) error {
	if value == "" || len(value) > 2048 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("lock release observed lock ID is invalid")
	}
	return nil
}

func validLockReleaseState(state LockReleaseState) bool {
	switch state {
	case LockReleasePending, LockReleaseDismissed, LockReleaseAccepted, LockReleaseLockGone, LockReleaseExpired, LockReleaseStale:
		return true
	default:
		return false
	}
}
