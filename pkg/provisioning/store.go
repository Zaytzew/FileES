package provisioning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	control "filees/pkg/control/v1"
)

const StateSchema = "filees.provisioning/v1"

type State string

const (
	StateLocalValidated            State = "local_validated"
	StateStoragePreflightRequested State = "storage_preflight_requested"
	StateStoragePreflightFailed    State = "storage_preflight_failed"
	StateStorageApproved           State = "storage_approved"
	StateProvisioningRequested     State = "provisioning_requested"
	StateRepositoryRequestFailed   State = "repository_request_failed"
	StateRepositoryReady           State = "repository_ready"
	StateInitialCommitInProgress   State = "initial_commit_in_progress"
	StateInitialCommitFailed       State = "initial_commit_failed"
	StateInitialSnapshotPublished  State = "initial_snapshot_published"
	StateActive                    State = "active"
)

type RequestStatus string

const (
	RequestPending RequestStatus = "pending"
	RequestOK      RequestStatus = "ok"
	RequestError   RequestStatus = "error"
)

type RequestRecord struct {
	Type         control.TicketType `json:"type"`
	Status       RequestStatus      `json:"status"`
	ResultDigest string             `json:"result_digest,omitempty"`
	Error        *control.ErrorBody `json:"error,omitempty"`
}

type Operation struct {
	Schema        string                   `json:"schema"`
	OperationID   string                   `json:"operation_id"`
	ClientID      string                   `json:"client_id"`
	LocalPath     string                   `json:"local_path"`
	Name          string                   `json:"name"`
	State         State                    `json:"state"`
	RepoID        string                   `json:"repo_id,omitempty"`
	RepoURL       string                   `json:"repo_url,omitempty"`
	Revision      int64                    `json:"revision,omitempty"`
	Paths         int                      `json:"paths,omitempty"`
	SnapshotBytes int64                    `json:"snapshot_bytes,omitempty"`
	SnapshotPaths int                      `json:"snapshot_paths,omitempty"`
	Requests      map[string]RequestRecord `json:"requests,omitempty"`
	CreatedAt     string                   `json:"created_at"`
	UpdatedAt     string                   `json:"updated_at"`
}

type Store struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
}

func NewStore(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("provisioning store root must be absolute")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create provisioning store: %w", err)
	}
	return &Store{root: root, now: time.Now}, nil
}

func (s *Store) CreateValidated(operationID, clientID, localPath, name string) (Operation, error) {
	return s.CreateValidatedSnapshot(operationID, clientID, localPath, name, 0, 0)
}

func (s *Store) CreateValidatedSnapshot(operationID, clientID, localPath, name string, snapshotBytes int64, snapshotPaths int) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := uuid.Parse(operationID); err != nil {
		return Operation{}, fmt.Errorf("operation_id must be UUID: %w", err)
	}
	clientID, name = strings.TrimSpace(clientID), strings.TrimSpace(name)
	if clientID == "" || name == "" {
		return Operation{}, errors.New("client_id and name are required")
	}
	if !filepath.IsAbs(localPath) {
		return Operation{}, errors.New("local_path must be absolute")
	}
	localPath = filepath.Clean(localPath)
	if snapshotBytes < 0 || snapshotPaths < 0 {
		return Operation{}, errors.New("snapshot size cannot be negative")
	}
	if existing, err := s.loadLocked(operationID); err == nil {
		if existing.ClientID == clientID && existing.LocalPath == localPath && existing.Name == name && existing.SnapshotBytes == snapshotBytes && existing.SnapshotPaths == snapshotPaths {
			return clone(existing), nil
		}
		return Operation{}, errors.New("operation_id already exists with different parameters")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Operation{}, err
	}
	now := s.timestamp()
	op := Operation{Schema: StateSchema, OperationID: operationID, ClientID: clientID, LocalPath: localPath, Name: name, SnapshotBytes: snapshotBytes, SnapshotPaths: snapshotPaths, State: StateLocalValidated, Requests: make(map[string]RequestRecord), CreatedAt: now, UpdatedAt: now}
	if err := s.saveLocked(op); err != nil {
		return Operation{}, err
	}
	return clone(op), nil
}

// RepairInitialImportInput retargets an import whose first checkout failed
// before any snapshot was published. CREATE_REPOSITORY and its request IDs
// remain durable; only the corrupted presentation input is corrected.
func (s *Store) RepairInitialImportInput(operationID, localPath, name string, snapshotBytes int64, snapshotPaths int) (Operation, error) {
	localPath, name = filepath.Clean(localPath), strings.TrimSpace(name)
	return s.mutate(operationID, func(op *Operation) error {
		if op.State != StateInitialCommitFailed || op.RepoID == "" || op.RepoURL == "" {
			return errors.New("only a failed initial import can be repaired")
		}
		if !filepath.IsAbs(localPath) || name == "" || strings.ContainsAny(name, "\x00\r\n") {
			return errors.New("repaired initial import identity or path is invalid")
		}
		if snapshotBytes < 0 || snapshotPaths < 0 {
			return errors.New("snapshot size cannot be negative")
		}
		op.LocalPath, op.Name = localPath, name
		op.SnapshotBytes, op.SnapshotPaths = snapshotBytes, snapshotPaths
		return nil
	})
}

func (s *Store) RequestStoragePreflight(operationID, requestID string) (Operation, error) {
	return s.mutate(operationID, func(op *Operation) error {
		if record, ok := op.Requests[requestID]; ok {
			if record.Type == control.TicketStoragePreflight {
				return nil
			}
			return errors.New("request_id already used for another ticket type")
		}
		if _, err := uuid.Parse(requestID); err != nil {
			return fmt.Errorf("request_id must be UUID: %w", err)
		}
		if op.State != StateLocalValidated && op.State != StateStoragePreflightFailed {
			return invalidTransition(op.State, StateStoragePreflightRequested)
		}
		op.Requests[requestID] = RequestRecord{Type: control.TicketStoragePreflight, Status: RequestPending}
		op.State = StateStoragePreflightRequested
		return nil
	})
}

func (s *Store) StoragePreflightTicket(operationID, requestID string) (control.Ticket, error) {
	op, err := s.Get(operationID)
	if err != nil {
		return control.Ticket{}, err
	}
	record, ok := op.Requests[requestID]
	if !ok || record.Type != control.TicketStoragePreflight || record.Status != RequestPending {
		return control.Ticket{}, errors.New("pending STORAGE_PREFLIGHT request is not registered")
	}
	return control.NewTicket(op.OperationID, requestID, control.TicketStoragePreflight, op.ClientID, control.StoragePreflightPayload{ContentBytes: op.SnapshotBytes, Paths: op.SnapshotPaths}, s.now())
}

func (s *Store) ApplyStoragePreflightResult(result control.Result) (Operation, error) {
	if err := result.Validate(); err != nil {
		return Operation{}, err
	}
	if result.Type != control.TicketStoragePreflight {
		return Operation{}, errors.New("expected STORAGE_PREFLIGHT result")
	}
	digest := resultDigest(result)
	return s.mutate(result.OperationID, func(op *Operation) error {
		record, ok := op.Requests[result.RequestID]
		if !ok || record.Type != control.TicketStoragePreflight {
			return errors.New("result does not match a registered request")
		}
		if record.Status != RequestPending {
			if record.ResultDigest == digest {
				return nil
			}
			return errors.New("conflicting result for an already completed request")
		}
		if op.State != StateStoragePreflightRequested {
			return invalidTransition(op.State, StateStorageApproved)
		}
		record.ResultDigest = digest
		if result.Status == control.ResultError {
			record.Status, record.Error, op.State = RequestError, cloneError(result.Error), StateStoragePreflightFailed
		} else {
			record.Status, op.State = RequestOK, StateStorageApproved
		}
		op.Requests[result.RequestID] = record
		return nil
	})
}

func (s *Store) RequestRepository(operationID, requestID string) (Operation, error) {
	return s.mutate(operationID, func(op *Operation) error {
		if record, ok := op.Requests[requestID]; ok {
			if record.Type == control.TicketCreateRepository {
				return nil
			}
			return errors.New("request_id already used for another ticket type")
		}
		if _, err := uuid.Parse(requestID); err != nil {
			return fmt.Errorf("request_id must be UUID: %w", err)
		}
		if op.State != StateStorageApproved && op.State != StateLocalValidated && op.State != StateRepositoryRequestFailed {
			return invalidTransition(op.State, StateProvisioningRequested)
		}
		op.Requests[requestID] = RequestRecord{Type: control.TicketCreateRepository, Status: RequestPending}
		op.State = StateProvisioningRequested
		return nil
	})
}

func (s *Store) CreateRepositoryTicket(operationID, requestID string) (control.Ticket, error) {
	op, err := s.Get(operationID)
	if err != nil {
		return control.Ticket{}, err
	}
	record, ok := op.Requests[requestID]
	if !ok || record.Type != control.TicketCreateRepository {
		return control.Ticket{}, errors.New("CREATE_REPOSITORY request is not registered")
	}
	return control.NewTicket(op.OperationID, requestID, control.TicketCreateRepository, op.ClientID, control.CreateRepositoryPayload{Name: op.Name}, s.now())
}

func (s *Store) ApplyRepositoryResult(result control.Result) (Operation, error) {
	if err := result.Validate(); err != nil {
		return Operation{}, err
	}
	if result.Type != control.TicketCreateRepository {
		return Operation{}, errors.New("expected CREATE_REPOSITORY result")
	}
	digest := resultDigest(result)
	return s.mutate(result.OperationID, func(op *Operation) error {
		record, ok := op.Requests[result.RequestID]
		if !ok || record.Type != control.TicketCreateRepository {
			return errors.New("result does not match a registered request")
		}
		if record.Status != RequestPending {
			if record.ResultDigest == digest {
				return nil
			}
			return errors.New("conflicting result for an already completed request")
		}
		if op.State != StateProvisioningRequested {
			return invalidTransition(op.State, StateRepositoryReady)
		}
		record.ResultDigest = digest
		if result.Status == control.ResultError {
			record.Status, record.Error = RequestError, cloneError(result.Error)
			op.Requests[result.RequestID] = record
			op.State = StateRepositoryRequestFailed
			return nil
		}
		var payload control.CreateRepositoryResult
		if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
			return err
		}
		record.Status = RequestOK
		op.Requests[result.RequestID] = record
		op.RepoID, op.RepoURL, op.State = payload.RepoID, payload.RepoURL, StateRepositoryReady
		return nil
	})
}

func (s *Store) StartInitialCommit(operationID, requestID string) (Operation, error) {
	return s.mutate(operationID, func(op *Operation) error {
		if record, ok := op.Requests[requestID]; ok {
			if record.Type == control.TicketInitialCommit {
				return nil
			}
			return errors.New("request_id already used for another ticket type")
		}
		if _, err := uuid.Parse(requestID); err != nil {
			return fmt.Errorf("request_id must be UUID: %w", err)
		}
		if op.State != StateRepositoryReady && op.State != StateInitialCommitFailed {
			return invalidTransition(op.State, StateInitialCommitInProgress)
		}
		op.Requests[requestID] = RequestRecord{Type: control.TicketInitialCommit, Status: RequestPending}
		op.State = StateInitialCommitInProgress
		return nil
	})
}

func (s *Store) FailInitialCommit(operationID, requestID, code, message string) (Operation, error) {
	return s.mutate(operationID, func(op *Operation) error {
		record, ok := op.Requests[requestID]
		if !ok || record.Type != control.TicketInitialCommit {
			return errors.New("INITIAL_COMMIT request is not registered")
		}
		code, message = strings.TrimSpace(code), strings.TrimSpace(message)
		if record.Status == RequestError && op.State == StateInitialCommitFailed {
			if record.Error != nil && record.Error.Code == code && record.Error.Message == message {
				return nil
			}
			return errors.New("conflicting failure for an already completed request")
		}
		if record.Status != RequestPending || op.State != StateInitialCommitInProgress {
			return invalidTransition(op.State, StateInitialCommitFailed)
		}
		record.Status = RequestError
		record.Error = &control.ErrorBody{Code: code, Message: message}
		if record.Error.Code == "" || record.Error.Message == "" {
			return errors.New("failure code and message are required")
		}
		op.Requests[requestID] = record
		op.State = StateInitialCommitFailed
		return nil
	})
}

func (s *Store) MarkInitialSnapshotPublished(operationID, requestID string, revision int64, paths int) (Operation, error) {
	return s.mutate(operationID, func(op *Operation) error {
		record, ok := op.Requests[requestID]
		if !ok || record.Type != control.TicketInitialCommit {
			return errors.New("INITIAL_COMMIT request is not registered")
		}
		if op.State == StateInitialSnapshotPublished && op.Revision == revision && op.Paths == paths {
			return nil
		}
		if op.State != StateInitialCommitInProgress || record.Status != RequestPending {
			return invalidTransition(op.State, StateInitialSnapshotPublished)
		}
		if revision < 0 || paths < 0 {
			return errors.New("revision and paths cannot be negative")
		}
		if revision == 0 && paths != 0 {
			return errors.New("revision zero is valid only for an empty snapshot")
		}
		op.Revision, op.Paths, op.State = revision, paths, StateInitialSnapshotPublished
		return nil
	})
}

func (s *Store) InitialCommitTicket(operationID, requestID string) (control.Ticket, error) {
	op, err := s.Get(operationID)
	if err != nil {
		return control.Ticket{}, err
	}
	if op.State != StateInitialSnapshotPublished {
		return control.Ticket{}, invalidTransition(op.State, StateInitialSnapshotPublished)
	}
	record, ok := op.Requests[requestID]
	if !ok || record.Type != control.TicketInitialCommit || record.Status != RequestPending {
		return control.Ticket{}, errors.New("pending INITIAL_COMMIT request is not registered")
	}
	return control.NewTicket(op.OperationID, requestID, control.TicketInitialCommit, op.ClientID, control.InitialCommitPayload{RepoID: op.RepoID, Revision: op.Revision, Paths: op.Paths}, s.now())
}

func (s *Store) ApplyInitialCommitResult(result control.Result) (Operation, error) {
	if err := result.Validate(); err != nil {
		return Operation{}, err
	}
	if result.Type != control.TicketInitialCommit {
		return Operation{}, errors.New("expected INITIAL_COMMIT result")
	}
	digest := resultDigest(result)
	return s.mutate(result.OperationID, func(op *Operation) error {
		record, ok := op.Requests[result.RequestID]
		if !ok || record.Type != control.TicketInitialCommit {
			return errors.New("result does not match a registered request")
		}
		if record.Status != RequestPending {
			if record.ResultDigest == digest {
				return nil
			}
			return errors.New("conflicting result for an already completed request")
		}
		if op.State != StateInitialSnapshotPublished {
			return invalidTransition(op.State, StateActive)
		}
		record.ResultDigest = digest
		if result.Status == control.ResultError {
			record.Status, record.Error = RequestError, cloneError(result.Error)
			op.Requests[result.RequestID] = record
			op.State = StateInitialCommitFailed
			return nil
		}
		record.Status = RequestOK
		op.Requests[result.RequestID] = record
		op.State = StateActive
		return nil
	})
}

func (s *Store) Get(operationID string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := s.loadLocked(operationID)
	return clone(op), err
}

func (s *Store) List() ([]Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	operations := make([]Operation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		operationID := strings.TrimSuffix(entry.Name(), ".json")
		op, err := s.loadLocked(operationID)
		if err != nil {
			return nil, fmt.Errorf("load provisioning operation %s: %w", operationID, err)
		}
		operations = append(operations, clone(op))
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].CreatedAt != operations[j].CreatedAt {
			return operations[i].CreatedAt < operations[j].CreatedAt
		}
		return operations[i].OperationID < operations[j].OperationID
	})
	return operations, nil
}

func (s *Store) mutate(operationID string, fn func(*Operation) error) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := s.loadLocked(operationID)
	if err != nil {
		return Operation{}, err
	}
	if op.Requests == nil {
		op.Requests = make(map[string]RequestRecord)
	}
	before, _ := json.Marshal(op)
	if err := fn(&op); err != nil {
		return Operation{}, err
	}
	after, _ := json.Marshal(op)
	if string(before) == string(after) {
		return clone(op), nil
	}
	op.UpdatedAt = s.timestamp()
	if err := s.saveLocked(op); err != nil {
		return Operation{}, err
	}
	return clone(op), nil
}

func (s *Store) loadLocked(operationID string) (Operation, error) {
	if _, err := uuid.Parse(operationID); err != nil {
		return Operation{}, fmt.Errorf("operation_id must be UUID: %w", err)
	}
	data, err := os.ReadFile(s.path(operationID))
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	if err := json.Unmarshal(data, &op); err != nil {
		return Operation{}, fmt.Errorf("decode provisioning operation: %w", err)
	}
	if err := validateOperation(op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (s *Store) saveLocked(op Operation) error {
	if err := validateOperation(op); err != nil {
		return err
	}
	data, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := s.path(op.OperationID) + "." + uuid.NewString() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path(op.OperationID)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(s.root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func validateOperation(op Operation) error {
	if op.Schema != StateSchema {
		return fmt.Errorf("unsupported provisioning schema %q", op.Schema)
	}
	if _, err := uuid.Parse(op.OperationID); err != nil {
		return fmt.Errorf("invalid operation_id: %w", err)
	}
	if op.ClientID == "" || op.Name == "" || !filepath.IsAbs(op.LocalPath) {
		return errors.New("invalid provisioning identity or path")
	}
	switch op.State {
	case StateLocalValidated, StateStoragePreflightRequested, StateStoragePreflightFailed, StateStorageApproved, StateProvisioningRequested, StateRepositoryRequestFailed,
		StateRepositoryReady, StateInitialCommitInProgress, StateInitialCommitFailed,
		StateInitialSnapshotPublished, StateActive:
	default:
		return fmt.Errorf("unsupported provisioning state %q", op.State)
	}
	if stateNeedsRepository(op.State) && (op.RepoID == "" || op.RepoURL == "") {
		return fmt.Errorf("state %s requires repository identity", op.State)
	}
	if (op.State == StateInitialSnapshotPublished || op.State == StateActive) && op.Revision < 0 {
		return fmt.Errorf("state %s requires published revision", op.State)
	}
	if (op.State == StateInitialSnapshotPublished || op.State == StateActive) && op.Revision == 0 && op.Paths != 0 {
		return fmt.Errorf("state %s has non-empty revision-zero snapshot", op.State)
	}
	for requestID, record := range op.Requests {
		if _, err := uuid.Parse(requestID); err != nil {
			return fmt.Errorf("invalid stored request_id %q", requestID)
		}
		if record.Type != control.TicketStoragePreflight && record.Type != control.TicketCreateRepository && record.Type != control.TicketInitialCommit {
			return fmt.Errorf("invalid stored request type %q", record.Type)
		}
		if record.Status != RequestPending && record.Status != RequestOK && record.Status != RequestError {
			return fmt.Errorf("invalid stored request status %q", record.Status)
		}
	}
	return nil
}

func stateNeedsRepository(state State) bool {
	switch state {
	case StateRepositoryReady, StateInitialCommitInProgress, StateInitialCommitFailed, StateInitialSnapshotPublished, StateActive:
		return true
	default:
		return false
	}
}

func (s *Store) path(operationID string) string { return filepath.Join(s.root, operationID+".json") }
func (s *Store) timestamp() string              { return s.now().UTC().Format(time.RFC3339Nano) }
func invalidTransition(from, to State) error {
	return fmt.Errorf("invalid provisioning transition %s -> %s", from, to)
}

func resultDigest(result control.Result) string {
	data, _ := json.Marshal(result)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func clone(op Operation) Operation {
	data, _ := json.Marshal(op)
	var out Operation
	_ = json.Unmarshal(data, &out)
	return out
}
func cloneError(in *control.ErrorBody) *control.ErrorBody {
	if in == nil {
		return nil
	}
	data, _ := json.Marshal(in)
	var out control.ErrorBody
	_ = json.Unmarshal(data, &out)
	return &out
}
