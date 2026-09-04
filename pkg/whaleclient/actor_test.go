package whaleclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

type actorExchange struct {
	mu             sync.Mutex
	content        []byte
	putOffset      int64
	disconnectOnce bool
	getWindows     int
	releases       int
}

type cancelExchange struct{ started chan struct{} }

func TestRemoveProfileRevokesFutureTransport(t *testing.T) {
	manager, err := NewManager(t.TempDir(), []clientprofile.Profile{{ServerID: "manual"}, {ServerID: "spot"}})
	if err != nil {
		t.Fatal(err)
	}
	manager.RemoveProfile("manual")
	if _, err := manager.profile("manual"); err == nil {
		t.Fatal("removed server profile still authorizes Whale transport")
	}
	if _, err := manager.profile("spot"); err != nil {
		t.Fatalf("unrelated profile was removed: %v", err)
	}
}

func (e *cancelExchange) Do(ctx context.Context, _ whale.Request, _ io.Reader, _ ...io.Writer) (whale.Response, error) {
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	<-ctx.Done()
	return whale.Response{}, ctx.Err()
}

func (e *actorExchange) Do(_ context.Context, request whale.Request, upload io.Reader, receive ...io.Writer) (whale.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := &whale.Result{GenerationID: request.Identity.GenerationID, TransferID: request.TransferID, Revision: request.Revision}
	switch request.Operation {
	case whale.OpPutWindow:
		if request.Offset != e.putOffset {
			return whale.Response{}, RemoteError{Body: whale.ErrorBody{Code: "WHALE-2003", Key: "whale.offset_conflict", Message: "offset"}}
		}
		if _, err := io.CopyN(io.Discard, upload, request.PayloadSize); err != nil {
			return whale.Response{}, err
		}
		e.putOffset += request.PayloadSize
		if e.disconnectOnce {
			e.disconnectOnce = false
			return whale.Response{}, errors.New("simulated disconnect after durable server write")
		}
		result.Offset, result.State = e.putOffset, whale.StateReceiving
		if e.putOffset == request.Identity.ExpectedSize {
			result.State = whale.StateCommitting
		}
	case whale.OpPutStatus:
		result.Offset, result.State = e.putOffset, whale.StateReceiving
	case whale.OpPutCommit:
		result.Offset, result.State, result.Revision = e.putOffset, whale.StatePublished, 19
	case whale.OpGetDiscover:
		digest := sha256.Sum256(e.content)
		identity := whale.Identity{LogicalRepoID: request.Identity.LogicalRepoID, LogicalPath: request.Identity.LogicalPath, GenerationID: uuid.NewString(), ExpectedSize: int64(len(e.content)), SHA256: hex.EncodeToString(digest[:])}
		result.GenerationID, result.State, result.Revision, result.ExpectedSize, result.SHA256, result.Identity = identity.GenerationID, whale.StateAwaitingConfirmation, 17, identity.ExpectedSize, identity.SHA256, &identity
	case whale.OpGetQuote:
		result.State, result.ExpectedSize, result.SHA256 = whale.StateAwaitingConfirmation, request.Identity.ExpectedSize, request.Identity.SHA256
	case whale.OpGetWindow:
		e.getWindows++
		if len(receive) != 1 {
			return whale.Response{}, errors.New("missing GET destination")
		}
		_, err := receive[0].Write(e.content[request.Offset : request.Offset+request.PayloadSize])
		if err != nil {
			return whale.Response{}, err
		}
		result.Offset, result.PayloadSize, result.State = request.Offset, request.PayloadSize, whale.StateMaterializing
	case whale.OpGetRelease:
		e.releases++
		result.State = whale.StateLocal
	default:
		return whale.Response{}, errors.New("unexpected operation")
	}
	return whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "ok", Result: result}, nil
}

func TestPutActorRecoversUnknownAckByStatusAndPersistsCompletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "whales")
	exchange := &actorExchange{disconnectOnce: true}
	manager := actorManager(t, root, exchange)
	manager.WindowSize = 3
	source := filepath.Join(t.TempDir(), "video.bin")
	if err := os.WriteFile(source, []byte("abcdefgh"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, err := manager.BeginPut(context.Background(), "office", uuid.NewString(), "media/video.bin", source)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitOperation(t, manager, op.OperationID, StateFailed)
	if failed.BytesHave != 0 || exchange.putOffset != 3 {
		t.Fatalf("failed=%+v server_offset=%d", failed, exchange.putOffset)
	}
	if _, err := manager.Retry(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	done := waitOperation(t, manager, op.OperationID, StatePublished)
	if done.BytesHave != 8 || done.PublishedRevision != 19 || exchange.putOffset != 8 {
		t.Fatalf("done=%+v server_offset=%d", done, exchange.putOffset)
	}
	if _, err := os.Stat(manager.spoolPath(op)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published spool survived: %v", err)
	}
	restarted := actorManager(t, root, exchange)
	loaded, err := restarted.Get(op.OperationID)
	if err != nil || loaded.State != StatePublished || loaded.BytesHave != 8 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestGetActorWaitsForConfirmationAndAtomicallyMaterializes(t *testing.T) {
	content := []byte("0123456789")
	digest := sha256.Sum256(content)
	identity := whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/video.bin", GenerationID: uuid.NewString(), ExpectedSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	exchange := &actorExchange{content: content}
	manager := actorManager(t, filepath.Join(t.TempDir(), "whales"), exchange)
	manager.WindowSize = 4
	destination := filepath.Join(t.TempDir(), "output", "video.bin")
	op, err := manager.BeginGetTarget(context.Background(), "office", identity.LogicalRepoID, identity.LogicalPath, 23, destination)
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, manager, op.OperationID, StateAwaitingConfirmation)
	if exchange.getWindows != 0 {
		t.Fatal("GET transferred bytes before confirmation")
	}
	if _, err := manager.ConfirmGet(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	done := waitOperation(t, manager, op.OperationID, StateLocal)
	raw, err := os.ReadFile(destination)
	if err != nil || string(raw) != string(content) {
		t.Fatalf("materialized=%q err=%v", raw, err)
	}
	if done.BytesHave != int64(len(content)) || exchange.getWindows != 3 || exchange.releases != 1 {
		t.Fatalf("done=%+v windows=%d releases=%d", done, exchange.getWindows, exchange.releases)
	}
	if _, err := os.Stat(done.PartialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial survived atomic rename: %v", err)
	}
}

func TestCancelledActorStateCannotBeOverwrittenByLateWindow(t *testing.T) {
	exchange := &cancelExchange{started: make(chan struct{})}
	manager := actorManager(t, filepath.Join(t.TempDir(), "whales"), exchange)
	source := filepath.Join(t.TempDir(), "large.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, err := manager.BeginPut(context.Background(), "office", uuid.NewString(), "media/large.bin", source)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-exchange.started:
	case <-time.After(5 * time.Second):
		t.Fatal("window did not start")
	}
	if _, err := manager.Cancel(op.OperationID, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cancelled, err := manager.Get(op.OperationID)
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
}

func TestGetRetryAdoptsVerifiedDestinationAfterRenameCrash(t *testing.T) {
	content := []byte("already renamed")
	digest := sha256.Sum256(content)
	identity := whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/final.bin", GenerationID: uuid.NewString(), ExpectedSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	exchange := &actorExchange{content: content}
	manager := actorManager(t, filepath.Join(t.TempDir(), "whales"), exchange)
	destination := filepath.Join(t.TempDir(), "final.bin")
	if err := os.WriteFile(destination, content, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	op := Operation{Schema: OperationSchema, OperationID: uuid.NewString(), ServerID: "office", Direction: whale.DirectionGet, LogicalRepoID: identity.LogicalRepoID, LogicalPath: identity.LogicalPath, GenerationID: identity.GenerationID, Identity: &identity, Revision: 5, ConfirmationToken: uuid.NewString(), DestinationPath: destination, PartialPath: destination + ".partial", State: StateFailed, BytesHave: identity.ExpectedSize, LastError: "simulated crash after rename", CreatedAt: now, UpdatedAt: now}
	if err := manager.save(op); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Retry(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	done := waitOperation(t, manager, op.OperationID, StateLocal)
	if done.LastError != "" || exchange.getWindows != 0 || exchange.releases != 1 {
		t.Fatalf("done=%+v windows=%d releases=%d", done, exchange.getWindows, exchange.releases)
	}
}

func TestResumePausesPutWhenBoundSpoolPayloadIsUnavailable(t *testing.T) {
	manager := actorManager(t, filepath.Join(t.TempDir(), "control"), &actorExchange{})
	identity := whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/missing.bin", GenerationID: uuid.NewString(), ExpectedSize: 7, SHA256: strings.Repeat("0", 64)}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	op := Operation{
		Schema: OperationSchema, OperationID: uuid.NewString(), ServerID: "office", Direction: whale.DirectionPut,
		LogicalRepoID: identity.LogicalRepoID, LogicalPath: identity.LogicalPath, GenerationID: identity.GenerationID,
		Identity: &identity, SourcePath: filepath.Join(t.TempDir(), "source.bin"), SpoolRoot: filepath.Join(t.TempDir(), "spool"),
		SpoolVolumeID: "volume-missing", SpoolDeviceID: "disk:9", ReservedBytes: identity.ExpectedSize,
		State: StateReady, CreatedAt: now, UpdatedAt: now,
	}
	if err := manager.save(op); err != nil {
		t.Fatal(err)
	}
	if err := manager.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	paused := waitOperation(t, manager, op.OperationID, StatePaused)
	if !strings.Contains(paused.LastError, "Whale spool payload unavailable") {
		t.Fatalf("paused=%+v", paused)
	}
}

func TestLegacyV1PutResumesFromItsOriginalControlRoot(t *testing.T) {
	content := []byte("legacy payload")
	digest := sha256.Sum256(content)
	exchange := &actorExchange{}
	manager := actorManager(t, filepath.Join(t.TempDir(), "control"), exchange)
	identity := whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/legacy.bin", GenerationID: uuid.NewString(), ExpectedSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	op := Operation{
		Schema: legacyOperationSchema, OperationID: uuid.NewString(), ServerID: "office", Direction: whale.DirectionPut,
		LogicalRepoID: identity.LogicalRepoID, LogicalPath: identity.LogicalPath, GenerationID: identity.GenerationID,
		Identity: &identity, SourcePath: filepath.Join(t.TempDir(), "legacy.bin"), State: StateFailed,
		LastError: "old daemon stopped", CreatedAt: now, UpdatedAt: now,
	}
	if err := manager.save(op); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.spoolPath(op), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Retry(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	done := waitOperation(t, manager, op.OperationID, StatePublished)
	if done.Schema != legacyOperationSchema || done.BytesHave != int64(len(content)) {
		t.Fatalf("done=%+v", done)
	}
}

func actorManager(t *testing.T, root string, exchange Exchanger) *Manager {
	t.Helper()
	manager, err := NewManager(root, []clientprofile.Profile{{ServerID: "office"}})
	if err != nil {
		t.Fatal(err)
	}
	manager.TransportFor = func(clientprofile.Profile) (Exchanger, error) { return exchange, nil }
	return manager
}

func waitOperation(t *testing.T, manager *Manager, operationID string, state OperationState) Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := manager.Get(operationID)
		if err == nil && op.State == state {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	op, err := manager.Get(operationID)
	t.Fatalf("operation did not reach %s: %+v err=%v", state, op, err)
	return Operation{}
}
