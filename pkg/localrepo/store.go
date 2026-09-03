// Package localrepo persists daemon-owned repository creation requests and
// local attachment intents. Server authority is deliberately not stored here.
package localrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/internal/durable"

	"github.com/google/uuid"
)

const Schema = "filees.local-repository-lifecycle/v1"

type State string

const (
	StateRequestPending State = "request_pending"
	// StateRepositoryCreated is the durable boundary after the server has
	// created the repository but before the initial snapshot is acknowledged.
	// Unlike StateError it continues to own its local path and repository ID:
	// retries must resume this operation, never mint another server repository.
	StateRepositoryCreated State = "repository_created"
	StateUnattached        State = "unattached"
	StatePolicyPending     State = "policy_pending"
	StateAttaching         State = "attaching"
	StateAttached          State = "attached"
	StateRelocating        State = "relocating"
	// StateReconciling is entered after LOAD_REPOSITORY_DUMP succeeds
	// server-side: the repository's UUID and entire history changed, so the
	// existing WC at LocalPath must be discarded and rechecked out in
	// place, never merged via a normal svn update
	// (LOAD_REPOSITORY_DUMP_CONCEPT.md §7). Unlike StateRelocating there is
	// no PendingLocalPath: the target path is the same as LocalPath.
	StateReconciling State = "reconciling"
	StateDetaching   State = "detaching"
	StateDeleting    State = "deleting"
	StateDetached    State = "detached"
	StateDeleted     State = "deleted"
	StateError       State = "error"
)

type Record struct {
	OperationID      string `json:"operation_id"`
	ServerID         string `json:"server_id"`
	RepoID           string `json:"repo_id,omitempty"`
	RepoURL          string `json:"repo_url,omitempty"`
	Access           string `json:"access,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
	LocalPath        string `json:"local_path"`
	PendingLocalPath string `json:"pending_local_path,omitempty"`
	// RelocationAdoptExisting distinguishes a user-selected moved WC from a
	// normal relocation. The provisioner must validate and adopt this path;
	// it must never checkout into or otherwise replace it.
	RelocationAdoptExisting bool   `json:"relocation_adopt_existing,omitempty"`
	DetachOperationID       string `json:"detach_operation_id,omitempty"`
	DeleteRepository        bool   `json:"delete_repository,omitempty"`
	// ServerDeleteCompleted is the durable semantic boundary between the
	// remote deletion and best-effort removal of local working-copy metadata.
	// Once set, retries must never issue DELETE_REPOSITORY again.
	ServerDeleteCompleted bool   `json:"server_delete_completed,omitempty"`
	RetainUntil           string `json:"retain_until,omitempty"`
	RecoveryPrepared      bool   `json:"recovery_prepared,omitempty"`
	RecoveryKitPath       string `json:"recovery_kit_path,omitempty"`
	LocalCleanupCompleted bool   `json:"local_cleanup_completed,omitempty"`
	// ReconcileOperationID is minted fresh by BeginReconcile and reused
	// across daemon restarts so the orchestration's own staging (named
	// after this ID, mirroring CreateFSFS's operationID convention) is
	// resumable instead of colliding with an abandoned prior attempt.
	ReconcileOperationID string `json:"reconcile_operation_id,omitempty"`
	// LoadDumpApplyIgnorePolicy/LoadDumpKeepLastRevisions are the user's
	// LOAD_REPOSITORY_DUMP options, persisted here (not just passed through
	// an in-memory enqueue) so a daemon restart mid-reconcile resumes with
	// the same options rather than losing them.
	LoadDumpApplyIgnorePolicy bool      `json:"load_dump_apply_ignore_policy,omitempty"`
	LoadDumpKeepLastRevisions *int      `json:"load_dump_keep_last_revisions,omitempty"`
	State                     State     `json:"state"`
	LastError                 string    `json:"last_error,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type document struct {
	Schema  string   `json:"schema"`
	Records []Record `json:"records"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	records map[string]Record
	now     func() time.Time
}

func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("local repository lifecycle path must be absolute")
	}
	s := &Store{path: filepath.Clean(path), records: make(map[string]Record), now: time.Now}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode local repository lifecycle: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("local repository lifecycle contains trailing data")
	}
	if doc.Schema != Schema {
		return nil, errors.New("local repository lifecycle schema is invalid")
	}
	for _, record := range doc.Records {
		// Historical StateDeleted records predate the three explicit receipts.
		// They could only be reached after both remote deletion and local
		// metadata removal, but no recoverable archive was issued. Migrate them
		// in memory as an already-expired capability; current in-progress
		// deletions still have to prove every boundary independently.
		if record.State == StateDeleted {
			if !record.ServerDeleteCompleted {
				record.ServerDeleteCompleted = true
				record.RetainUntil = record.UpdatedAt.UTC().Format(time.RFC3339Nano)
				record.RecoveryPrepared = true
				record.RecoveryKitPath = ""
			}
			record.LocalCleanupCompleted = true
		}
		if err := validate(record); err != nil {
			return nil, err
		}
		if _, exists := s.records[record.OperationID]; exists {
			return nil, errors.New("duplicate local repository operation")
		}
		s.records[record.OperationID] = record
	}
	return s, nil
}

func (s *Store) BeginCreate(serverID, displayName, localPath string) (Record, error) {
	return s.BeginCreateOperation(uuid.NewString(), serverID, displayName, localPath)
}

func (s *Store) BeginCreateOperation(operationID, serverID, displayName, localPath string) (Record, error) {
	return s.begin(Record{OperationID: operationID, ServerID: serverID, DisplayName: strings.TrimSpace(displayName), LocalPath: localPath, State: StateRequestPending})
}

func (s *Store) BeginAttach(serverID, repoID, localPath string, required bool) (Record, error) {
	state := StateUnattached
	if required {
		state = StatePolicyPending
	}
	// The folder's own name is kept from the start, which is the only moment it
	// is certainly available.
	//
	// BeginCreate has always recorded one; BeginAttach - the path taken when a
	// folder already on disk is adopted - recorded nothing, so the record knew
	// the repository only by UUID. That is invisible while the server's view
	// carries the name, and it is the case that reached the owner: after the
	// repository was deleted the view stopped carrying it, and his interface
	// showed a bare UID exactly where he had to decide whether to download the
	// archive. A record whose whole purpose is to outlive the server's
	// knowledge should not depend on the server for what to call itself.
	return s.begin(Record{
		ServerID: serverID, RepoID: repoID, LocalPath: localPath,
		DisplayName: folderName(localPath), State: state,
	})
}

// folderName is the presentable name of a working copy, or empty when the path
// carries none. It never invents one: an unnamable path leaves DisplayName
// unset so the reader falls back deliberately rather than on a guess.
func folderName(localPath string) string {
	trimmed := strings.TrimSpace(localPath)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(trimmed))
	if base == "." || base == string(filepath.Separator) || base == filepath.VolumeName(trimmed)+string(filepath.Separator) {
		return ""
	}
	return base
}

// EnsureConfiguredAttached imports a legacy/static config entry into the
// durable lifecycle store. A detach/delete tombstone wins over the unchanged
// config file, so restarting the daemon cannot silently reattach that WC.
func (s *Store) EnsureConfiguredAttached(serverID, repoID, repoURL, access, localPath, displayName string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var tombstone Record
	for _, existing := range s.records {
		if existing.ServerID != serverID || existing.RepoID != repoID {
			continue
		}
		switch existing.State {
		case StateDetached, StateDeleted:
			if tombstone.OperationID == "" || existing.UpdatedAt.After(tombstone.UpdatedAt) {
				tombstone = existing
			}
		case StateError:
			continue
		default:
			return existing, false, nil
		}
	}
	if tombstone.OperationID != "" {
		return tombstone, false, nil
	}
	record, err := s.beginLocked(Record{
		ServerID: serverID, RepoID: repoID, RepoURL: repoURL, Access: access,
		DisplayName: strings.TrimSpace(displayName), LocalPath: localPath, State: StateAttached,
	})
	return record, err == nil, err
}

func (s *Store) begin(record Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginLocked(record)
}

func (s *Store) beginLocked(record Record) (Record, error) {
	if record.OperationID == "" {
		record.OperationID = uuid.NewString()
	}
	record.LocalPath = filepath.Clean(record.LocalPath)
	record.CreatedAt = s.now().UTC()
	record.UpdatedAt = record.CreatedAt
	if err := validate(record); err != nil {
		return Record{}, err
	}
	for _, existing := range s.records {
		if terminal(existing.State) {
			// A terminal error claims no live checkout or attachment. Counting
			// it here would let one failed attempt (e.g. a transient
			// STORAGE_INSUFFICIENT that later clears server-side) permanently
			// block every future attempt at the same path or repo ID.
			continue
		}
		if existing.ServerID == record.ServerID && record.RepoID != "" && existing.RepoID == record.RepoID {
			return Record{}, errors.New("repository already has a local lifecycle record")
		}
		if pathsOverlap(existing.LocalPath, record.LocalPath) {
			return Record{}, errors.New("local path overlaps another FileES repository root")
		}
	}
	s.records[record.OperationID] = record
	if err := s.persist(); err != nil {
		delete(s.records, record.OperationID)
		return Record{}, err
	}
	return record, nil
}

func terminal(state State) bool {
	return state == StateError || state == StateDetached || state == StateDeleted
}

func (s *Store) MarkAttached(operationID, repoID string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State == StateAttached && record.RepoID == repoID {
			return nil
		}
		if record.State != StateRequestPending && record.State != StateRepositoryCreated && record.State != StateAttaching && record.State != StateError {
			return errors.New("local repository operation cannot become attached")
		}
		if strings.TrimSpace(repoID) == "" || strings.ContainsAny(repoID, "/\\\x00\r\n\t ") {
			return errors.New("repository ID is invalid")
		}
		record.RepoID, record.State, record.LastError = repoID, StateAttached, ""
		return nil
	})
}

func (s *Store) MarkRepositoryCreated(operationID, repoID, repoURL string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		repoID, repoURL = strings.TrimSpace(repoID), strings.TrimSpace(repoURL)
		if record.State == StateRepositoryCreated && record.RepoID == repoID && record.RepoURL == repoURL {
			return nil
		}
		if record.State != StateRequestPending && record.State != StateRepositoryCreated && record.State != StateError {
			return errors.New("local repository operation cannot record a created repository")
		}
		if repoID == "" || strings.ContainsAny(repoID, "/\\\x00\r\n\t ") {
			return errors.New("created repository ID is invalid")
		}
		if !strings.HasPrefix(repoURL, "svn+ssh://") {
			return errors.New("created repository URL is invalid")
		}
		record.RepoID, record.RepoURL, record.Access = repoID, repoURL, "rw"
		record.State, record.LastError = StateRepositoryCreated, ""
		return nil
	})
}

// ResumeCreate clears the last provisioning failure without discarding the
// durable server-repository boundary. The daemon will reuse the provisioning
// journal and its existing repo_id/request IDs.
func (s *Store) ResumeCreate(operationID string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateRepositoryCreated {
			return errors.New("only a created repository can resume initial import")
		}
		record.LastError = ""
		return nil
	})
}

// RepairCreatedRepositoryInput corrects user input that was corrupted before
// it crossed the daemon IPC boundary. It is intentionally limited to the
// durable post-CREATE_REPOSITORY/pre-import boundary: the server repository
// identity is retained, while no attached working copy can be silently moved.
func (s *Store) RepairCreatedRepositoryInput(operationID, displayName, localPath string) (Record, error) {
	displayName, localPath = strings.TrimSpace(displayName), filepath.Clean(localPath)
	return s.update(operationID, func(record *Record) error {
		if record.State != StateRepositoryCreated || record.RepoID == "" || record.RepoURL == "" {
			return errors.New("only a created repository awaiting initial import can be repaired")
		}
		if displayName == "" || strings.ContainsAny(displayName, "\x00\r\n") {
			return errors.New("repository display name is invalid")
		}
		if !filepath.IsAbs(localPath) || localPath == string(filepath.Separator) {
			return errors.New("repaired repository path must be an absolute non-root path")
		}
		for id, existing := range s.records {
			if id != operationID && existing.State != StateError && existing.State != StateDetached && existing.State != StateDeleted && pathsOverlap(existing.LocalPath, localPath) {
				return errors.New("repaired repository path overlaps another FileES repository root")
			}
		}
		record.DisplayName, record.LocalPath, record.LastError = displayName, localPath, ""
		return nil
	})
}

func (s *Store) ApproveAttach(operationID, serverID, repoID, repoURL, access string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.ServerID != serverID || record.RepoID != repoID {
			return errors.New("attachment approval does not match the persisted intent")
		}
		if record.State == StateAttaching && record.RepoURL == repoURL && record.Access == access {
			return nil
		}
		if record.State != StateUnattached && record.State != StatePolicyPending && record.State != StateError {
			return errors.New("local repository operation cannot start attachment")
		}
		record.State, record.LastError, record.RepoURL, record.Access = StateAttaching, "", repoURL, access
		return nil
	})
}

func (s *Store) MarkError(operationID string, cause error) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if cause == nil || strings.TrimSpace(cause.Error()) == "" {
			return errors.New("local repository error is required")
		}
		// Once CREATE_REPOSITORY succeeded, the record must keep owning both
		// its local path and server repository identity. Downgrading it to the
		// generic StateError would allow a retry to create a second orphan.
		if record.State != StateRepositoryCreated {
			record.State = StateError
		}
		record.LastError = cause.Error()
		return nil
	})
}

func (s *Store) BeginRelocation(serverID, repoID, newLocalPath string) (Record, error) {
	return s.beginRelocation(serverID, repoID, newLocalPath, false)
}

func (s *Store) BeginLocate(serverID, repoID, existingLocalPath string) (Record, error) {
	return s.beginRelocation(serverID, repoID, existingLocalPath, true)
}

func (s *Store) beginRelocation(serverID, repoID, newLocalPath string, adoptExisting bool) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	newLocalPath = filepath.Clean(newLocalPath)
	var operationID string
	for id, record := range s.records {
		if record.ServerID == serverID && record.RepoID == repoID {
			operationID = id
			break
		}
	}
	if operationID == "" {
		return Record{}, os.ErrNotExist
	}
	record := s.records[operationID]
	if record.State == StateRelocating && record.PendingLocalPath == newLocalPath && record.RelocationAdoptExisting == adoptExisting {
		return record, nil
	}
	if record.State != StateAttached {
		return Record{}, errors.New("only an attached repository can be relocated")
	}
	if !filepath.IsAbs(newLocalPath) || newLocalPath == string(filepath.Separator) {
		return Record{}, errors.New("relocation target must be an absolute disjoint non-root path")
	}
	// Locate may reaffirm the current root after the drive or mount becomes
	// available again. A true relocation must remain disjoint.
	if pathsOverlap(record.LocalPath, newLocalPath) && !(adoptExisting && filepath.Clean(newLocalPath) == filepath.Clean(record.LocalPath)) {
		return Record{}, errors.New("relocation target must be an absolute disjoint non-root path")
	}
	for id, existing := range s.records {
		if id != operationID && pathsOverlap(existing.LocalPath, newLocalPath) {
			return Record{}, errors.New("relocation target overlaps another FileES repository root")
		}
	}
	before := record
	record.State, record.PendingLocalPath, record.RelocationAdoptExisting, record.LastError, record.UpdatedAt = StateRelocating, newLocalPath, adoptExisting, "", s.now().UTC()
	if err := validate(record); err != nil {
		return Record{}, err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return Record{}, err
	}
	return record, nil
}

func (s *Store) CompleteRelocation(operationID string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateRelocating || record.PendingLocalPath == "" {
			return errors.New("repository relocation is not in progress")
		}
		record.LocalPath, record.PendingLocalPath, record.RelocationAdoptExisting, record.State, record.LastError = record.PendingLocalPath, "", false, StateAttached, ""
		return nil
	})
}

func (s *Store) FailRelocation(operationID string, cause error) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateRelocating || cause == nil || strings.TrimSpace(cause.Error()) == "" {
			return errors.New("active relocation and failure are required")
		}
		record.State, record.PendingLocalPath, record.RelocationAdoptExisting, record.LastError = StateAttached, "", false, cause.Error()
		return nil
	})
}

// BeginReconcile starts (or resumes, idempotently) the recheckout-in-place
// forced by a successful LOAD_REPOSITORY_DUMP. It never touches LocalPath —
// only the orchestration layer does, and only after building and verifying
// the replacement WC elsewhere first (LOAD_REPOSITORY_DUMP_CONCEPT.md §7).
func (s *Store) BeginReconcile(serverID, repoID string, applyIgnorePolicy bool, keepLastRevisions *int) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var operationID string
	for id, record := range s.records {
		if record.ServerID == serverID && record.RepoID == repoID && !terminal(record.State) {
			operationID = id
			break
		}
	}
	if operationID == "" {
		return Record{}, os.ErrNotExist
	}
	record := s.records[operationID]
	if record.State == StateReconciling && record.ReconcileOperationID != "" {
		return record, nil // resume the same attempt, not a new one
	}
	if record.State != StateAttached {
		return Record{}, errors.New("only an attached repository can be reconciled")
	}
	before := record
	record.State = StateReconciling
	record.ReconcileOperationID = uuid.NewString()
	record.LoadDumpApplyIgnorePolicy = applyIgnorePolicy
	record.LoadDumpKeepLastRevisions = keepLastRevisions
	record.LastError = ""
	record.UpdatedAt = s.now().UTC()
	if err := validate(record); err != nil {
		return Record{}, err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return Record{}, err
	}
	return record, nil
}

func (s *Store) CompleteReconcile(operationID string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State == StateAttached {
			return nil // idempotent replay after an already-completed reconcile
		}
		if record.State != StateReconciling {
			return errors.New("repository reconcile is not in progress")
		}
		record.State, record.ReconcileOperationID, record.LastError = StateAttached, "", ""
		record.LoadDumpApplyIgnorePolicy, record.LoadDumpKeepLastRevisions = false, nil
		return nil
	})
}

// FailReconcile is only valid before the orchestration's replacement WC has
// been swapped into LocalPath — the old WC is still there and still good,
// so returning to StateAttached is safe, the same guarantee FailRelocation
// gives. A crash after the swap but before CompleteReconcile is not a
// FailReconcile case: on restart the orchestration resumes the same
// ReconcileOperationID and must detect the swap already happened rather
// than repeat it (LOAD_REPOSITORY_DUMP_CONCEPT.md §8).
func (s *Store) FailReconcile(operationID string, cause error) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateReconciling || cause == nil || strings.TrimSpace(cause.Error()) == "" {
			return errors.New("active reconcile and failure are required")
		}
		record.State, record.ReconcileOperationID, record.LastError = StateAttached, "", cause.Error()
		record.LoadDumpApplyIgnorePolicy, record.LoadDumpKeepLastRevisions = false, nil
		return nil
	})
}

func (s *Store) BeginDetach(serverID, repoID string, deleteRepository bool) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var operationID string
	for id, record := range s.records {
		if record.ServerID == serverID && record.RepoID == repoID && !terminal(record.State) {
			operationID = id
			break
		}
	}
	if operationID == "" {
		return Record{}, os.ErrNotExist
	}
	record := s.records[operationID]
	targetState := StateDetaching
	if deleteRepository {
		targetState = StateDeleting
	}
	if record.State == targetState && record.DeleteRepository == deleteRepository && record.DetachOperationID != "" {
		return record, nil
	}
	if record.State != StateAttached {
		return Record{}, errors.New("only an attached repository can be detached")
	}
	before := record
	record.State = targetState
	record.DetachOperationID = uuid.NewString()
	record.DeleteRepository = deleteRepository
	record.ServerDeleteCompleted = false
	record.RetainUntil = ""
	record.RecoveryPrepared = false
	record.RecoveryKitPath = ""
	record.LocalCleanupCompleted = false
	record.LastError = ""
	record.UpdatedAt = s.now().UTC()
	if err := validate(record); err != nil {
		return Record{}, err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return Record{}, err
	}
	return record, nil
}

// MarkServerDeleted records the server-authoritative result before any local
// metadata is touched. It is idempotent so a daemon restart can safely resume
// cleanup without replaying the destructive server request.
func (s *Store) MarkServerDeleted(operationID, retainUntil string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateDeleting || !record.DeleteRepository {
			return errors.New("repository deletion is not in progress")
		}
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(retainUntil))
		if err != nil {
			return errors.New("repository deletion retention deadline is invalid")
		}
		canonical := parsed.UTC().Format(time.RFC3339Nano)
		if record.ServerDeleteCompleted {
			if record.RetainUntil != canonical {
				return errors.New("repository deletion retention conflicts with prior receipt")
			}
			return nil
		}
		record.ServerDeleteCompleted = true
		record.RetainUntil = canonical
		record.LastError = ""
		return nil
	})
}

// MarkRecoveryPrepared binds the deleted repository to its local recovery
// capability. An empty kit path is valid when server retention is disabled
// and therefore no archive exists.
func (s *Store) MarkRecoveryPrepared(operationID, kitPath string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateDeleting || !record.ServerDeleteCompleted {
			return errors.New("server repository deletion is not complete")
		}
		if kitPath != "" && !filepath.IsAbs(kitPath) {
			return errors.New("repository recovery kit path must be absolute")
		}
		if record.RecoveryPrepared {
			if record.RecoveryKitPath != kitPath {
				return errors.New("repository recovery capability conflicts with prior receipt")
			}
			return nil
		}
		record.RecoveryPrepared = true
		record.RecoveryKitPath = filepath.Clean(kitPath)
		if kitPath == "" {
			record.RecoveryKitPath = ""
		}
		record.LastError = ""
		return nil
	})
}

func (s *Store) MarkLocalCleanupCompleted(operationID string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateDeleting || !record.ServerDeleteCompleted {
			return errors.New("server repository deletion is not complete")
		}
		record.LocalCleanupCompleted = true
		return nil
	})
}

func (s *Store) CompleteDetach(operationID string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		switch record.State {
		case StateDetaching:
			record.State = StateDetached
		case StateDeleting:
			record.State = StateDeleted
		case StateDetached, StateDeleted:
			return nil
		default:
			return errors.New("repository detach is not in progress")
		}
		record.LastError = ""
		return nil
	})
}

func (s *Store) RecordDetachError(operationID string, cause error) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State != StateDetaching && record.State != StateDeleting {
			return errors.New("repository detach is not in progress")
		}
		if cause == nil || strings.TrimSpace(cause.Error()) == "" {
			return errors.New("repository detach error is required")
		}
		record.LastError = cause.Error()
		return nil
	})
}

func (s *Store) update(operationID string, mutate func(*Record) error) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok {
		return Record{}, os.ErrNotExist
	}
	before := record
	if err := mutate(&record); err != nil {
		return Record{}, err
	}
	if record == before {
		return record, nil
	}
	record.UpdatedAt = s.now().UTC()
	if err := validate(record); err != nil {
		return Record{}, err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return Record{}, err
	}
	return record, nil
}

func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OperationID < out[j].OperationID })
	return out
}

func (s *Store) Get(operationID string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	return record, ok
}

func validate(r Record) error {
	if _, err := uuid.Parse(r.OperationID); err != nil {
		return errors.New("local repository operation ID must be UUID")
	}
	if strings.TrimSpace(r.ServerID) == "" || strings.ContainsAny(r.ServerID, "/\\\x00\r\n\t ") {
		return errors.New("local repository server ID is invalid")
	}
	if !filepath.IsAbs(r.LocalPath) || filepath.Clean(r.LocalPath) == string(filepath.Separator) {
		return errors.New("local repository path must be an absolute non-root path")
	}
	if r.RepoID == "" && r.DisplayName == "" {
		return errors.New("repository creation display name is required")
	}
	if r.RepoID != "" && strings.ContainsAny(r.RepoID, "/\\\x00\r\n\t ") {
		return errors.New("local repository ID is invalid")
	}
	if r.RepoURL != "" || r.Access != "" || r.State == StateAttaching {
		if !strings.HasPrefix(r.RepoURL, "svn+ssh://") || (r.Access != "r" && r.Access != "rw") {
			return errors.New("attached repository authority is invalid")
		}
	}
	switch r.State {
	case StateRequestPending, StateRepositoryCreated, StateUnattached, StatePolicyPending, StateAttaching, StateAttached, StateRelocating, StateReconciling, StateDetaching, StateDeleting, StateDetached, StateDeleted, StateError:
	default:
		return errors.New("local repository lifecycle state is invalid")
	}
	if r.State == StateRepositoryCreated {
		if r.RepoID == "" || r.RepoURL == "" || r.Access != "rw" {
			return errors.New("created repository lifecycle is incomplete")
		}
	}
	if r.State == StateRelocating {
		if !filepath.IsAbs(r.PendingLocalPath) || r.PendingLocalPath == string(filepath.Separator) {
			return errors.New("repository relocation target is invalid")
		}
		sameRoot := filepath.Clean(r.LocalPath) == filepath.Clean(r.PendingLocalPath)
		if pathsOverlap(r.LocalPath, r.PendingLocalPath) && !(r.RelocationAdoptExisting && sameRoot) {
			return errors.New("repository relocation target is invalid")
		}
	} else if r.PendingLocalPath != "" || r.RelocationAdoptExisting {
		return errors.New("repository relocation target exists outside relocation")
	}
	if r.State == StateReconciling {
		if _, err := uuid.Parse(r.ReconcileOperationID); err != nil {
			return errors.New("repository reconcile operation ID must be UUID")
		}
	} else if r.ReconcileOperationID != "" || r.LoadDumpApplyIgnorePolicy || r.LoadDumpKeepLastRevisions != nil {
		return errors.New("repository reconcile metadata exists outside reconcile")
	}
	if r.LoadDumpKeepLastRevisions != nil && *r.LoadDumpKeepLastRevisions < 1 {
		return errors.New("load dump keep_last_revisions must be at least 1")
	}
	if r.State == StateDetaching || r.State == StateDeleting || r.State == StateDetached || r.State == StateDeleted {
		if _, err := uuid.Parse(r.DetachOperationID); err != nil {
			return errors.New("repository detach operation ID must be UUID")
		}
		if (r.State == StateDeleting || r.State == StateDeleted) != r.DeleteRepository {
			return errors.New("repository deletion state is inconsistent")
		}
	} else if r.DetachOperationID != "" || r.DeleteRepository {
		return errors.New("repository detach metadata exists outside detach")
	}
	if r.ServerDeleteCompleted {
		if !r.DeleteRepository || (r.State != StateDeleting && r.State != StateDeleted) {
			return errors.New("server deletion receipt exists outside repository deletion")
		}
		if _, err := time.Parse(time.RFC3339Nano, r.RetainUntil); err != nil {
			return errors.New("repository deletion retention deadline is invalid")
		}
	} else if r.RetainUntil != "" || r.RecoveryPrepared || r.RecoveryKitPath != "" {
		return errors.New("repository recovery metadata exists before server deletion")
	}
	if r.RecoveryPrepared && r.RecoveryKitPath != "" && !filepath.IsAbs(r.RecoveryKitPath) {
		return errors.New("repository recovery kit path must be absolute")
	}
	if r.LocalCleanupCompleted && (!r.ServerDeleteCompleted || !r.DeleteRepository || (r.State != StateDeleting && r.State != StateDeleted)) {
		return errors.New("local deletion cleanup receipt exists outside repository deletion")
	}
	if r.State == StateDeleted && (!r.ServerDeleteCompleted || !r.RecoveryPrepared || !r.LocalCleanupCompleted) {
		return errors.New("completed repository deletion is missing a durable boundary")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return errors.New("local repository lifecycle timestamps are invalid")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

func (s *Store) persist() error {
	doc := document{Schema: Schema, Records: make([]Record, 0, len(s.records))}
	for _, r := range s.records {
		doc.Records = append(doc.Records, r)
	}
	sort.Slice(doc.Records, func(i, j int) bool { return doc.Records[i].OperationID < doc.Records[j].OperationID })
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".lifecycle-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(s.path))
}
