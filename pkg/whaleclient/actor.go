package whaleclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"filees/pkg/clientprofile"
	"filees/pkg/privatefile"
	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

const (
	OperationSchema   = "filees.whale-operation/v1"
	DefaultWindowSize = int64(64 << 20)
	clientSafetyFloor = int64(64 << 20)
)

type OperationState string

const (
	StatePreparing            OperationState = "preparing"
	StateReady                OperationState = "ready"
	StateTransferring         OperationState = "transferring"
	StateCommitting           OperationState = "committing"
	StatePublished            OperationState = "published"
	StateQuoting              OperationState = "quoting"
	StateAwaitingConfirmation OperationState = "awaiting_confirmation"
	StateMaterializing        OperationState = "materializing"
	StateVerifying            OperationState = "verifying"
	StateLocal                OperationState = "local"
	StatePaused               OperationState = "paused"
	StateCancelled            OperationState = "cancelled"
	StateFailed               OperationState = "failed"
)

type Operation struct {
	Schema            string          `json:"schema"`
	OperationID       string          `json:"operation_id"`
	ServerID          string          `json:"server_id"`
	Direction         whale.Direction `json:"direction"`
	LogicalRepoID     string          `json:"logical_repo_id"`
	LogicalPath       string          `json:"logical_path"`
	GenerationID      string          `json:"generation_id"`
	Identity          *whale.Identity `json:"identity,omitempty"`
	Revision          int64           `json:"revision,omitempty"`
	ConfirmationToken string          `json:"confirmation_token,omitempty"`
	SourcePath        string          `json:"source_path,omitempty"`
	DestinationPath   string          `json:"destination_path,omitempty"`
	PartialPath       string          `json:"partial_path,omitempty"`
	State             OperationState  `json:"state"`
	BytesHave         int64           `json:"bytes_have"`
	PublishedRevision int64           `json:"published_revision,omitempty"`
	LastError         string          `json:"last_error,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

type Exchanger interface {
	Do(context.Context, whale.Request, io.Reader, ...io.Writer) (whale.Response, error)
}

type Manager struct {
	Root       string
	WindowSize int64
	OnChange   func(Operation)
	// TransportFor is an adapter seam for tests and alternate embedded
	// runtimes. Nil selects the pinned desktop SSH transport.
	TransportFor func(clientprofile.Profile) (Exchanger, error)

	mu       sync.Mutex
	profiles map[string]clientprofile.Profile
	cancels  map[string]context.CancelFunc
}

func DefaultRoot() string {
	return filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "whales")
}

func NewManager(root string, profiles []clientprofile.Profile) (*Manager, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("Whale operation root must be absolute")
	}
	manager := &Manager{Root: filepath.Clean(root), WindowSize: DefaultWindowSize, profiles: make(map[string]clientprofile.Profile), cancels: make(map[string]context.CancelFunc)}
	for _, profile := range profiles {
		manager.profiles[profile.ServerID] = profile
	}
	if err := privatefile.EnsureDir(manager.Root); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) AddProfile(profile clientprofile.Profile) {
	m.mu.Lock()
	m.profiles[profile.ServerID] = profile
	m.mu.Unlock()
}

func (m *Manager) BeginPut(ctx context.Context, serverID, logicalRepoID, logicalPath, sourcePath string) (Operation, error) {
	if _, err := uuid.Parse(logicalRepoID); err != nil {
		return Operation{}, errors.New("logical repository ID must be UUID")
	}
	if err := whale.ValidateLogicalPath(logicalPath); err != nil {
		return Operation{}, err
	}
	if !filepath.IsAbs(sourcePath) {
		return Operation{}, errors.New("Whale source path must be absolute")
	}
	if _, err := m.profile(serverID); err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	op := Operation{Schema: OperationSchema, OperationID: uuid.NewString(), ServerID: serverID, Direction: whale.DirectionPut, LogicalRepoID: logicalRepoID, LogicalPath: logicalPath, GenerationID: uuid.NewString(), SourcePath: filepath.Clean(sourcePath), State: StatePreparing, CreatedAt: now, UpdatedAt: now}
	if err := m.save(op); err != nil {
		return Operation{}, err
	}
	m.launch(ctx, op.OperationID, m.runPut)
	return op, nil
}

// BeginGet persists the intended destination before asking for a quote. The
// quote creates no server cache; only ConfirmGet may enter materialization.
func (m *Manager) BeginGet(ctx context.Context, serverID string, identity whale.Identity, revision int64, destinationPath string) (Operation, error) {
	if err := identity.Validate(); err != nil {
		return Operation{}, err
	}
	if revision < 1 || !filepath.IsAbs(destinationPath) {
		return Operation{}, errors.New("Whale GET revision and destination are invalid")
	}
	if _, err := os.Stat(destinationPath); err == nil {
		return Operation{}, errors.New("Whale destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Operation{}, err
	}
	if _, err := m.profile(serverID); err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	opID := uuid.NewString()
	destinationPath = filepath.Clean(destinationPath)
	op := Operation{Schema: OperationSchema, OperationID: opID, ServerID: serverID, Direction: whale.DirectionGet, LogicalRepoID: identity.LogicalRepoID, LogicalPath: identity.LogicalPath, GenerationID: identity.GenerationID, Identity: &identity, Revision: revision, DestinationPath: destinationPath, PartialPath: destinationPath + ".filees-whale-" + opID + ".partial", State: StateQuoting, CreatedAt: now, UpdatedAt: now}
	if err := m.save(op); err != nil {
		return Operation{}, err
	}
	m.launch(ctx, op.OperationID, m.runQuote)
	return op, nil
}

func (m *Manager) BeginGetTarget(ctx context.Context, serverID, logicalRepoID, logicalPath string, snapshotRevision int64, destinationPath string) (Operation, error) {
	if !canonicalOperationUUID(logicalRepoID) || whale.ValidateLogicalPath(logicalPath) != nil || snapshotRevision < 1 || !filepath.IsAbs(destinationPath) {
		return Operation{}, errors.New("Whale GET target is invalid")
	}
	if _, err := os.Stat(destinationPath); err == nil {
		return Operation{}, errors.New("Whale destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Operation{}, err
	}
	if _, err := m.profile(serverID); err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	opID := uuid.NewString()
	destinationPath = filepath.Clean(destinationPath)
	op := Operation{Schema: OperationSchema, OperationID: opID, ServerID: serverID, Direction: whale.DirectionGet, LogicalRepoID: logicalRepoID, LogicalPath: logicalPath, Revision: snapshotRevision, DestinationPath: destinationPath, PartialPath: destinationPath + ".filees-whale-" + opID + ".partial", State: StateQuoting, CreatedAt: now, UpdatedAt: now}
	if err := m.save(op); err != nil {
		return Operation{}, err
	}
	m.launch(ctx, op.OperationID, m.runQuote)
	return op, nil
}

func (m *Manager) ConfirmGet(ctx context.Context, operationID string) (Operation, error) {
	op, err := m.Get(operationID)
	if err != nil {
		return Operation{}, err
	}
	if op.Direction != whale.DirectionGet || op.State != StateAwaitingConfirmation || op.Identity == nil {
		return Operation{}, errors.New("Whale GET is not awaiting confirmation")
	}
	if err := ensureSpace(filepath.Dir(op.DestinationPath), op.Identity.ExpectedSize-op.BytesHave); err != nil {
		return Operation{}, err
	}
	op.ConfirmationToken = uuid.NewString()
	op.State, op.LastError = StateMaterializing, ""
	if err := m.save(op); err != nil {
		return Operation{}, err
	}
	m.launch(ctx, op.OperationID, m.runGet)
	return op, nil
}

func (m *Manager) Cancel(operationID string, removePayload bool) (Operation, error) {
	m.mu.Lock()
	if cancel := m.cancels[operationID]; cancel != nil {
		cancel()
	}
	m.mu.Unlock()
	op, err := m.Get(operationID)
	if err != nil {
		return Operation{}, err
	}
	if op.State == StatePublished || op.State == StateLocal {
		return Operation{}, errors.New("completed Whale operation cannot be cancelled")
	}
	op.State, op.LastError = StateCancelled, ""
	if err := m.save(op); err != nil {
		return Operation{}, err
	}
	if removePayload {
		_ = os.Remove(m.spoolPath(operationID))
		_ = os.Remove(op.PartialPath)
	}
	return op, nil
}

func (m *Manager) Retry(ctx context.Context, operationID string) (Operation, error) {
	op, err := m.Get(operationID)
	if err != nil {
		return Operation{}, err
	}
	if op.State != StateFailed && op.State != StatePaused {
		return Operation{}, errors.New("Whale operation is not retryable")
	}
	if err := m.waitIdle(ctx, operationID); err != nil {
		return Operation{}, err
	}
	op.LastError = ""
	if op.Direction == whale.DirectionPut {
		if op.Identity == nil {
			op.State = StatePreparing
		} else if op.BytesHave == op.Identity.ExpectedSize {
			op.State = StateCommitting
		} else {
			op.State = StateReady
		}
		if err := m.save(op); err != nil {
			return Operation{}, err
		}
		m.launch(ctx, op.OperationID, m.runPut)
	} else {
		if op.ConfirmationToken == "" {
			op.State = StateQuoting
			if err := m.save(op); err != nil {
				return Operation{}, err
			}
			m.launch(ctx, op.OperationID, m.runQuote)
		} else {
			op.State = StateMaterializing
			if err := m.save(op); err != nil {
				return Operation{}, err
			}
			m.launch(ctx, op.OperationID, m.runGet)
		}
	}
	return op, nil
}

// Resume starts every durable non-terminal operation. Awaiting confirmation,
// paused and failed records remain presentation-only until a new user intent.
func (m *Manager) Resume(ctx context.Context) error {
	operations, err := m.List()
	if err != nil {
		return err
	}
	for _, op := range operations {
		switch op.State {
		case StatePreparing, StateReady, StateTransferring, StateCommitting:
			m.launch(ctx, op.OperationID, m.runPut)
		case StateQuoting:
			m.launch(ctx, op.OperationID, m.runQuote)
		case StateMaterializing, StateVerifying:
			m.launch(ctx, op.OperationID, m.runGet)
		case StatePublished:
			_ = os.Remove(m.spoolPath(op.OperationID))
		case StateLocal:
			_ = os.Remove(op.PartialPath)
		}
	}
	return nil
}

func (m *Manager) Get(operationID string) (Operation, error) {
	if id, err := uuid.Parse(operationID); err != nil || id.String() != operationID {
		return Operation{}, errors.New("Whale operation ID must be a canonical UUID")
	}
	raw, err := os.ReadFile(m.statePath(operationID))
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&op); err != nil {
		return Operation{}, err
	}
	if err := validateOperation(op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (m *Manager) List() ([]Operation, error) {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		return nil, err
	}
	var operations []Operation
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		op, err := m.Get(entry.Name())
		if err != nil {
			return nil, err
		}
		operations = append(operations, op)
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].CreatedAt < operations[j].CreatedAt })
	return operations, nil
}

func (m *Manager) runPut(ctx context.Context, operationID string) {
	op, err := m.Get(operationID)
	if err != nil || op.State == StateCancelled {
		return
	}
	if op.State == StatePreparing {
		op, err = m.capture(ctx, op)
		if err != nil {
			m.fail(op, err)
			return
		}
	}
	transport, err := m.transport(op.ServerID)
	if err != nil {
		m.fail(op, err)
		return
	}
	for op.BytesHave < op.Identity.ExpectedSize {
		if err := ctx.Err(); err != nil {
			return
		}
		op.State = StateTransferring
		if err := m.save(op); err != nil {
			m.fail(op, err)
			return
		}
		count := min(m.windowSize(), op.Identity.ExpectedSize-op.BytesHave)
		file, err := os.Open(m.spoolPath(op.OperationID))
		if err != nil {
			m.fail(op, err)
			return
		}
		_, err = file.Seek(op.BytesHave, io.SeekStart)
		if err != nil {
			file.Close()
			m.fail(op, err)
			return
		}
		request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutWindow, Identity: *op.Identity, Offset: op.BytesHave, PayloadSize: count}
		response, exchangeErr := transport.Do(ctx, request, file)
		file.Close()
		if exchangeErr != nil {
			var remote RemoteError
			if errors.As(exchangeErr, &remote) && remote.Body.Key == "whale.offset_conflict" {
				if err := m.reconcilePutOffset(ctx, transport, &op); err == nil {
					continue
				}
			}
			m.fail(op, exchangeErr)
			return
		}
		op.BytesHave = response.Result.Offset
		if err := m.save(op); err != nil {
			m.fail(op, err)
			return
		}
	}
	op.State = StateCommitting
	if err := m.save(op); err != nil {
		m.fail(op, err)
		return
	}
	request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutCommit, Identity: *op.Identity}
	response, err := transport.Do(ctx, request, nil)
	if err != nil {
		m.fail(op, err)
		return
	}
	op.State, op.PublishedRevision, op.LastError = StatePublished, response.Result.Revision, ""
	if err := m.save(op); err != nil {
		m.fail(op, err)
		return
	}
	_ = os.Remove(m.spoolPath(op.OperationID))
}

func (m *Manager) capture(ctx context.Context, op Operation) (Operation, error) {
	source, err := os.Open(op.SourcePath)
	if err != nil {
		return op, err
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > whale.MaxObjectBytes {
		return op, errors.New("Whale source must be a regular file within the size limit")
	}
	if err := ensureSpace(m.Root, before.Size()); err != nil {
		return op, err
	}
	dir := m.operationDir(op.OperationID)
	if err := privatefile.EnsureDir(dir); err != nil {
		return op, err
	}
	tmp, err := os.CreateTemp(dir, ".capture-*.tmp")
	if err != nil {
		return op, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), &contextReader{ctx: ctx, reader: source})
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return op, err
	}
	after, err := os.Stat(op.SourcePath)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || written != before.Size() {
		return op, errors.New("Whale source changed during capture")
	}
	identity := whale.Identity{LogicalRepoID: op.LogicalRepoID, LogicalPath: op.LogicalPath, GenerationID: op.GenerationID, ExpectedSize: written, SHA256: hex.EncodeToString(hash.Sum(nil))}
	if err := identity.Validate(); err != nil {
		return op, err
	}
	if err := os.Rename(tmpPath, m.spoolPath(op.OperationID)); err != nil {
		return op, err
	}
	if err := durable.SyncDirectory(dir); err != nil {
		return op, err
	}
	op.Identity, op.State, op.BytesHave, op.LastError = &identity, StateReady, 0, ""
	return op, m.save(op)
}

func (m *Manager) reconcilePutOffset(ctx context.Context, transport Exchanger, op *Operation) error {
	request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutStatus, Identity: *op.Identity}
	response, err := transport.Do(ctx, request, nil)
	if err != nil {
		return err
	}
	if response.Result.Offset < op.BytesHave || response.Result.Offset > op.Identity.ExpectedSize {
		return errors.New("server Whale offset conflicts with acknowledged local state")
	}
	op.BytesHave = response.Result.Offset
	return m.save(*op)
}

func (m *Manager) runQuote(ctx context.Context, operationID string) {
	op, err := m.Get(operationID)
	if err != nil || op.State == StateCancelled {
		return
	}
	transport, err := m.transport(op.ServerID)
	if err == nil {
		if op.Identity == nil {
			request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetDiscover, Identity: whale.Identity{LogicalRepoID: op.LogicalRepoID, LogicalPath: op.LogicalPath}, Revision: op.Revision}
			var response whale.Response
			response, err = transport.Do(ctx, request, nil)
			if err == nil && response.Result.Identity != nil {
				identity := *response.Result.Identity
				op.Identity, op.GenerationID, op.Revision = &identity, identity.GenerationID, response.Result.Revision
			} else if err == nil {
				err = errors.New("Whale discovery returned no identity")
			}
		} else {
			request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetQuote, Identity: *op.Identity, Revision: op.Revision}
			_, err = transport.Do(ctx, request, nil)
		}
	}
	if err != nil {
		m.fail(op, err)
		return
	}
	op.State, op.LastError = StateAwaitingConfirmation, ""
	if err := m.save(op); err != nil {
		m.fail(op, err)
	}
}

func (m *Manager) runGet(ctx context.Context, operationID string) {
	op, err := m.Get(operationID)
	if err != nil || op.Identity == nil || op.State == StateCancelled {
		return
	}
	transport, err := m.transport(op.ServerID)
	if err != nil {
		m.fail(op, err)
		return
	}
	if recovered, recoverErr := m.recoverCompletedGet(ctx, transport, &op); recoverErr != nil {
		m.fail(op, recoverErr)
		return
	} else if recovered {
		return
	}
	if err := os.MkdirAll(filepath.Dir(op.DestinationPath), 0o700); err != nil {
		m.fail(op, err)
		return
	}
	file, err := os.OpenFile(op.PartialPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		m.fail(op, err)
		return
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.Size() < op.BytesHave {
		m.fail(op, errors.New("Whale GET partial is shorter than durable state"))
		return
	} else if info.Size() > op.BytesHave {
		if err := file.Truncate(op.BytesHave); err != nil {
			m.fail(op, err)
			return
		}
	}
	for op.BytesHave < op.Identity.ExpectedSize {
		if err := ctx.Err(); err != nil {
			return
		}
		count := min(m.windowSize(), op.Identity.ExpectedSize-op.BytesHave)
		if _, err := file.Seek(op.BytesHave, io.SeekStart); err != nil {
			m.fail(op, err)
			return
		}
		request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetWindow, Identity: *op.Identity, Revision: op.Revision, TransferID: op.OperationID, ConfirmationToken: op.ConfirmationToken, Offset: op.BytesHave, PayloadSize: count}
		response, err := transport.Do(ctx, request, nil, file)
		if err != nil {
			m.fail(op, err)
			return
		}
		if err := file.Sync(); err != nil {
			m.fail(op, err)
			return
		}
		op.BytesHave = response.Result.Offset + response.Result.PayloadSize
		op.State = StateMaterializing
		if err := m.save(op); err != nil {
			m.fail(op, err)
			return
		}
	}
	op.State = StateVerifying
	if err := m.save(op); err != nil {
		m.fail(op, err)
		return
	}
	if err := file.Close(); err != nil {
		m.fail(op, err)
		return
	}
	digest, size, err := digestPath(op.PartialPath)
	if err != nil || size != op.Identity.ExpectedSize || digest != op.Identity.SHA256 {
		m.fail(op, errors.Join(err, errors.New("Whale GET digest mismatch")))
		return
	}
	if _, err := os.Stat(op.DestinationPath); err == nil {
		m.fail(op, errors.New("Whale destination appeared during download"))
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		m.fail(op, err)
		return
	}
	if err := os.Rename(op.PartialPath, op.DestinationPath); err != nil {
		m.fail(op, err)
		return
	}
	if err := durable.SyncDirectory(filepath.Dir(op.DestinationPath)); err != nil {
		m.fail(op, err)
		return
	}
	op.State, op.LastError = StateLocal, ""
	if err := m.save(op); err != nil {
		m.fail(op, err)
		return
	}
	m.releaseGet(ctx, transport, op)
}

// recoverCompletedGet closes the crash boundary after destination rename but
// before the local state snapshot. The immutable digest makes adopting that
// exact destination safe; any other file remains a conflict.
func (m *Manager) recoverCompletedGet(ctx context.Context, transport Exchanger, op *Operation) (bool, error) {
	if op.Identity == nil || op.BytesHave != op.Identity.ExpectedSize {
		return false, nil
	}
	if _, err := os.Stat(op.PartialPath); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := os.Stat(op.DestinationPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	digest, size, err := digestPath(op.DestinationPath)
	if err != nil || size != op.Identity.ExpectedSize || digest != op.Identity.SHA256 {
		return false, errors.Join(err, errors.New("Whale destination conflicts with completed transfer"))
	}
	op.State, op.LastError = StateLocal, ""
	if err := m.save(*op); err != nil {
		return false, err
	}
	m.releaseGet(ctx, transport, *op)
	return true, nil
}

func (m *Manager) releaseGet(ctx context.Context, transport Exchanger, op Operation) {
	release := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpGetRelease, Identity: *op.Identity, Revision: op.Revision, TransferID: op.OperationID, ConfirmationToken: op.ConfirmationToken}
	_, _ = transport.Do(context.WithoutCancel(ctx), release, nil)
}

func (m *Manager) launch(parent context.Context, operationID string, run func(context.Context, string)) {
	m.mu.Lock()
	if _, exists := m.cancels[operationID]; exists {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancels[operationID] = cancel
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.cancels, operationID)
			m.mu.Unlock()
		}()
		run(ctx, operationID)
	}()
}

func (m *Manager) fail(op Operation, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	current, loadErr := m.Get(op.OperationID)
	if loadErr == nil && current.State == StateCancelled {
		return
	}
	op.State, op.LastError = StateFailed, err.Error()
	_ = m.save(op)
}

func (m *Manager) save(op Operation) error {
	if err := validateOperation(op); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if raw, readErr := os.ReadFile(m.statePath(op.OperationID)); readErr == nil {
		var current Operation
		if json.Unmarshal(raw, &current) == nil && (current.State == StateCancelled || current.State == StatePublished || current.State == StateLocal) && current.State != op.State {
			return errors.New("Whale terminal state cannot be overwritten")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	op.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	dir := m.operationDir(op.OperationID)
	if err := privatefile.EnsureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = privatefile.Harden(tmpPath)
	}
	if err == nil {
		err = replaceActorState(tmpPath, m.statePath(op.OperationID))
	}
	if err == nil {
		err = durable.SyncDirectory(dir)
	}
	if err == nil && m.OnChange != nil {
		m.OnChange(op)
	}
	return err
}

// Windows can transiently return ERROR_ACCESS_DENIED when a renderer is
// opening the old snapshot at the same instant as replacement. The actor has
// one writer, so a short bounded retry preserves atomic last-state-wins
// semantics without masking a real ACL error.
func replaceActorState(tmpPath, path string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.Rename(tmpPath, path); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return err
}

func (m *Manager) waitIdle(ctx context.Context, operationID string) error {
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		_, running := m.cancels[operationID]
		m.mu.Unlock()
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("previous Whale attempt is still stopping")
		case <-ticker.C:
		}
	}
}

func validateOperation(op Operation) error {
	if op.Schema != OperationSchema {
		return errors.New("unsupported Whale operation schema")
	}
	if id, err := uuid.Parse(op.OperationID); err != nil || id.String() != op.OperationID || strings.TrimSpace(op.ServerID) == "" {
		return errors.New("invalid Whale operation identity")
	}
	if id, err := uuid.Parse(op.LogicalRepoID); err != nil || id.String() != op.LogicalRepoID || whale.ValidateLogicalPath(op.LogicalPath) != nil {
		return errors.New("invalid Whale logical target")
	}
	if op.GenerationID == "" {
		if op.Direction != whale.DirectionGet || (op.State != StateQuoting && op.State != StateFailed && op.State != StateCancelled) || op.Identity != nil {
			return errors.New("invalid empty Whale generation ID")
		}
	} else if !canonicalOperationUUID(op.GenerationID) {
		return errors.New("invalid Whale generation ID")
	}
	if op.Identity != nil && (op.Identity.LogicalRepoID != op.LogicalRepoID || op.Identity.LogicalPath != op.LogicalPath || op.Identity.GenerationID != op.GenerationID || op.Identity.Validate() != nil) {
		return errors.New("invalid Whale operation content identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, op.CreatedAt); err != nil {
		return errors.New("invalid Whale operation timestamp")
	}
	return nil
}

func canonicalOperationUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func (m *Manager) profile(serverID string) (clientprofile.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	profile, ok := m.profiles[serverID]
	if !ok {
		return clientprofile.Profile{}, fmt.Errorf("Whale server profile %q is unavailable", serverID)
	}
	return profile, nil
}

func (m *Manager) transport(serverID string) (Exchanger, error) {
	profile, err := m.profile(serverID)
	if err != nil {
		return nil, err
	}
	if m.TransportFor != nil {
		return m.TransportFor(profile)
	}
	return NewTransport(Config{Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile, KnownHosts: profile.KnownHosts, Timeout: profile.SVNTimeout()})
}

func (m *Manager) operationDir(id string) string { return filepath.Join(m.Root, id) }
func (m *Manager) statePath(id string) string    { return filepath.Join(m.operationDir(id), "state.json") }
func (m *Manager) spoolPath(id string) string {
	return filepath.Join(m.operationDir(id), "payload.ready")
}
func (m *Manager) windowSize() int64 {
	if m.WindowSize > 0 && m.WindowSize <= whale.MaxWindowBytes {
		return m.WindowSize
	}
	return DefaultWindowSize
}

func ensureSpace(path string, contentBytes int64) error {
	if contentBytes < 0 {
		return errors.New("Whale space requirement is invalid")
	}
	probe := filepath.Clean(path)
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return errors.New("Whale filesystem root is unavailable")
		}
		probe = parent
	}
	available, err := filesystemAvailable(probe)
	if err != nil {
		return err
	}
	margin := clientSafetyFloor
	if contentBytes/20 > margin {
		margin = contentBytes / 20
	}
	if contentBytes > available-margin {
		return fmt.Errorf("insufficient space for Whale: available=%d required=%d safety=%d", available, contentBytes, margin)
	}
	return nil
}

func digestPath(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	return hex.EncodeToString(hash.Sum(nil)), size, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(out []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(out)
}
