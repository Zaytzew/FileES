package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type effects struct {
	fsfs, publish int
	authorize     int
	failAuthorize bool
	failPublish   bool
	rollback      int
	failRollback  bool
	failPrepare   bool
	failArchive   bool
	restore       int
	deleteSteps   []string
}

func (e *effects) CreateFSFS(context.Context, string, string) error { e.fsfs++; return nil }
func (e *effects) PublishAuthority(context.Context, string, string, string, string, string) error {
	e.publish++
	if e.failPublish {
		return errors.New("crash boundary")
	}
	return nil
}
func (e *effects) RollbackCreate(context.Context, string, string) error {
	e.rollback++
	if e.failRollback {
		return errors.New("rollback boundary")
	}
	return nil
}
func (e *effects) AuthorizeDelete(context.Context, string, string) error {
	e.authorize++
	if e.failAuthorize {
		return errors.New("authenticated realm does not own repository")
	}
	return nil
}
func (e *effects) PrepareDelete(context.Context, string, string) error {
	e.deleteSteps = append(e.deleteSteps, "blocked")
	if e.failPrepare {
		return errors.New("prepare boundary")
	}
	return nil
}
func (e *effects) RestoreDelete(context.Context, string, string) error {
	e.restore++
	e.deleteSteps = append(e.deleteSteps, "restored")
	return nil
}
func (e *effects) WithdrawAuthority(context.Context, string, string) error {
	e.deleteSteps = append(e.deleteSteps, "withdrawn")
	return nil
}
func (e *effects) ArchiveAndDeleteFSFS(context.Context, string, string) (time.Time, error) {
	e.deleteSteps = append(e.deleteSteps, "archive")
	if e.failArchive {
		return time.Time{}, errors.New("archive boundary")
	}
	return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC), nil
}

func TestDurableBackendResumesRepositoryDeletionBoundaries(t *testing.T) {
	fx := &effects{failArchive: true}
	backend := &DurableBackend{Root: t.TempDir(), Effects: fx}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := backend.Delete(context.Background(), operationID, realmID, repoID); err == nil {
		t.Fatal("archive boundary failure missing")
	}
	fx.failArchive = false
	retainUntil, err := backend.Delete(context.Background(), operationID, realmID, repoID)
	if err != nil {
		t.Fatal(err)
	}
	wantSteps := []string{"blocked", "withdrawn", "archive", "archive"}
	if len(fx.deleteSteps) != len(wantSteps) {
		t.Fatalf("delete steps=%v", fx.deleteSteps)
	}
	for index := range wantSteps {
		if fx.deleteSteps[index] != wantSteps[index] {
			t.Fatalf("delete steps=%v, want %v", fx.deleteSteps, wantSteps)
		}
	}
	if retainUntil.IsZero() {
		t.Fatal("retention timestamp missing")
	}
	before := len(fx.deleteSteps)
	if replay, err := backend.Delete(context.Background(), operationID, realmID, repoID); err != nil || !replay.Equal(retainUntil) {
		t.Fatalf("delete replay=%s err=%v", replay, err)
	}
	if len(fx.deleteSteps) != before {
		t.Fatalf("completed delete replayed effects: %v", fx.deleteSteps)
	}
}

func TestDurableBackendRestoresFailedDeletePreparationAndRetries(t *testing.T) {
	fx := &effects{failPrepare: true}
	backend := &DurableBackend{Root: t.TempDir(), Effects: fx}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := backend.Delete(context.Background(), operationID, realmID, repoID); err == nil || !strings.Contains(err.Error(), "prepare boundary") {
		t.Fatalf("prepare failure=%v", err)
	}
	if got := strings.Join(fx.deleteSteps, ","); got != "blocked,restored" || fx.restore != 1 {
		t.Fatalf("failed preparation effects=%v restore=%d", fx.deleteSteps, fx.restore)
	}
	raw, err := os.ReadFile(filepath.Join(backend.Root, "delete-"+operationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record deleteBackendRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Stage != "allocated" {
		t.Fatalf("failed preparation stage=%q", record.Stage)
	}

	fx.failPrepare = false
	if _, err := backend.Delete(context.Background(), operationID, realmID, repoID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fx.deleteSteps, ","); got != "blocked,restored,blocked,withdrawn,archive" {
		t.Fatalf("retry effects=%v", fx.deleteSteps)
	}
}

func TestDurableBackendReapsOnlyAllocatedDeletesThenRetriesSameOperation(t *testing.T) {
	fx := &effects{}
	backend := &DurableBackend{Root: t.TempDir(), Effects: fx}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	records := []deleteBackendRecord{
		{OperationID: operationID, RealmID: realmID, RepoID: repoID, Stage: "allocated"},
		{OperationID: uuid.NewString(), RealmID: uuid.NewString(), RepoID: uuid.NewString(), Stage: "blocked"},
		{OperationID: uuid.NewString(), RealmID: uuid.NewString(), RepoID: uuid.NewString(), Stage: "withdrawn"},
	}
	for _, record := range records {
		path := filepath.Join(backend.Root, "delete-"+record.OperationID+".json")
		if err := backend.saveDelete(path, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := backend.ReapUncommittedDeletes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fx.restore != 1 || strings.Join(fx.deleteSteps, ",") != "restored" {
		t.Fatalf("reaper effects=%v restore=%d", fx.deleteSteps, fx.restore)
	}
	if _, err := backend.Delete(context.Background(), operationID, realmID, repoID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fx.deleteSteps, ","); got != "restored,blocked,withdrawn,archive" {
		t.Fatalf("same-operation retry effects=%v", fx.deleteSteps)
	}
}

func TestDurableBackendDeleteReaperRejectsCorruptCanonicalRecord(t *testing.T) {
	fx := &effects{}
	backend := &DurableBackend{Root: t.TempDir(), Effects: fx}
	operationID := uuid.NewString()
	record := deleteBackendRecord{OperationID: uuid.NewString(), RealmID: uuid.NewString(), RepoID: uuid.NewString(), Stage: "allocated"}
	if err := backend.saveDelete(filepath.Join(backend.Root, "delete-"+operationID+".json"), record); err != nil {
		t.Fatal(err)
	}
	if err := backend.ReapUncommittedDeletes(context.Background()); err == nil || !strings.Contains(err.Error(), "conflicts with its filename") {
		t.Fatalf("corrupt record error=%v", err)
	}
	if fx.restore != 0 {
		t.Fatalf("corrupt record reached restoration: %+v", fx)
	}
}

func TestDurableBackendRejectsForeignDeleteBeforeBlockingHook(t *testing.T) {
	fx := &effects{failAuthorize: true}
	backend := &DurableBackend{Root: t.TempDir(), Effects: fx}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := backend.Delete(context.Background(), operationID, realmID, repoID); err == nil {
		t.Fatal("foreign deletion was accepted")
	}
	if fx.authorize != 1 || len(fx.deleteSteps) != 0 {
		t.Fatalf("foreign deletion reached destructive effects: %+v", fx)
	}
	if _, err := os.Stat(filepath.Join(backend.Root, "delete-"+operationID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign deletion left a durable record: %v", err)
	}
}
func TestDurableBackendRollsBackAfterAuthorityFailure(t *testing.T) {
	fx := &effects{failPublish: true}
	b := &DurableBackend{Root: t.TempDir(), URLPrefix: "svn+ssh://_filees-client@example/repos/", Effects: fx}
	op, realm := uuid.NewString(), uuid.NewString()
	if _, e := b.Create(context.Background(), op, realm, "Docs"); e == nil {
		t.Fatal("failure missing")
	}
	if fx.fsfs != 1 || fx.publish != 1 || fx.rollback != 1 {
		t.Fatalf("effects after failed publish: %+v", fx)
	}
	if _, err := os.Stat(filepath.Join(b.Root, op+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed backend record remains: %v", err)
	}
	fx.failPublish = false
	r, e := b.Create(context.Background(), op, realm, "Docs")
	if e != nil {
		t.Fatal(e)
	}
	if fx.fsfs != 2 || fx.publish != 2 || fx.rollback != 1 || r.RepoID == "" {
		t.Fatalf("fsfs=%d publish=%d rollback=%d repo=%+v", fx.fsfs, fx.publish, fx.rollback, r)
	}
}

func TestDurableBackendResumesPendingRollbackWithoutRepublishing(t *testing.T) {
	fx := &effects{failPublish: true, failRollback: true}
	b := &DurableBackend{Root: t.TempDir(), URLPrefix: "svn+ssh://_filees-client@example/repos/", Effects: fx}
	op, realm := uuid.NewString(), uuid.NewString()
	if _, err := b.Create(context.Background(), op, realm, "Docs"); err == nil {
		t.Fatal("publish and rollback failure missing")
	}
	fx.failRollback = false
	if _, err := b.Create(context.Background(), op, realm, "Docs"); err == nil || !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("rollback resume error = %v", err)
	}
	if fx.fsfs != 1 || fx.publish != 1 || fx.rollback != 2 {
		t.Fatalf("pending rollback retried publication: %+v", fx)
	}
}

func TestDurableBackendReapsPendingRollbackOnLaterWorkerRun(t *testing.T) {
	fx := &effects{failPublish: true, failRollback: true}
	b := &DurableBackend{Root: t.TempDir(), URLPrefix: "svn+ssh://_filees-client@example/repos/", Effects: fx}
	op, realm := uuid.NewString(), uuid.NewString()
	if _, err := b.Create(context.Background(), op, realm, "Docs"); err == nil {
		t.Fatal("publish and rollback failure missing")
	}
	fx.failRollback = false
	if err := b.ReapFailedCreates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fx.fsfs != 1 || fx.publish != 1 || fx.rollback != 2 {
		t.Fatalf("reaper retried creation instead of rollback: %+v", fx)
	}
	if _, err := os.Stat(filepath.Join(b.Root, op+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reaped backend record remains: %v", err)
	}
}

func TestDurableBackendRejectsInvalidURLPrefixBeforeFSFS(t *testing.T) {
	fx := &effects{}
	b := &DurableBackend{Root: t.TempDir(), URLPrefix: "svn+ssh://_filees-client@example:2223/repos/", Effects: fx}
	if _, err := b.Create(context.Background(), uuid.NewString(), uuid.NewString(), "Docs"); err == nil {
		t.Fatal("invalid URL prefix accepted")
	}
	if fx.fsfs != 0 || fx.publish != 0 || fx.rollback != 0 {
		t.Fatalf("invalid URL prefix reached effects: %+v", fx)
	}
}
