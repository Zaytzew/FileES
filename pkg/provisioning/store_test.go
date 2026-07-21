package provisioning

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	control "filees/pkg/control/v1"
)

func newTestStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 7, 15, 1, 2, 3, 4, time.UTC) }
	return store
}

func result(t *testing.T, opID, reqID string, typ control.TicketType, payload any) control.Result {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return control.Result{Schema: control.Schema, OperationID: opID, RequestID: reqID, Type: typ, Status: control.ResultOK, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: raw}
}

func TestStoreFullLifecycleSurvivesRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "provisioning")
	store := newTestStore(t, root)
	opID, createReq, initialReq := uuid.NewString(), uuid.NewString(), uuid.NewString()
	local := filepath.Join(t.TempDir(), "project")

	op, err := store.CreateValidated(opID, "client-a", local, "Project A")
	if err != nil || op.State != StateLocalValidated {
		t.Fatalf("CreateValidated = %#v, %v", op, err)
	}
	op, err = store.RequestRepository(opID, createReq)
	if err != nil || op.State != StateProvisioningRequested {
		t.Fatalf("RequestRepository = %#v, %v", op, err)
	}
	ticket, err := store.CreateRepositoryTicket(opID, createReq)
	if err != nil || ticket.Type != control.TicketCreateRepository {
		t.Fatalf("ticket = %#v, %v", ticket, err)
	}

	store = newTestStore(t, root)
	op, err = store.ApplyRepositoryResult(result(t, opID, createReq, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: "repo-a", RepoURL: "svn://example/repo-a"}))
	if err != nil || op.State != StateRepositoryReady || op.RepoID != "repo-a" {
		t.Fatalf("repository result = %#v, %v", op, err)
	}
	op, err = store.StartInitialCommit(opID, initialReq)
	if err != nil || op.State != StateInitialCommitInProgress {
		t.Fatalf("StartInitialCommit = %#v, %v", op, err)
	}
	op, err = store.MarkInitialSnapshotPublished(opID, initialReq, 75, 12)
	if err != nil || op.State != StateInitialSnapshotPublished {
		t.Fatalf("published = %#v, %v", op, err)
	}
	ticket, err = store.InitialCommitTicket(opID, initialReq)
	if err != nil || ticket.Type != control.TicketInitialCommit {
		t.Fatalf("initial ticket = %#v, %v", ticket, err)
	}
	op, err = store.ApplyInitialCommitResult(result(t, opID, initialReq, control.TicketInitialCommit, control.InitialCommitResult{Acknowledged: true}))
	if err != nil || op.State != StateActive {
		t.Fatalf("active = %#v, %v", op, err)
	}

	store = newTestStore(t, root)
	loaded, err := store.Get(opID)
	if err != nil || loaded.State != StateActive || loaded.Revision != 75 || loaded.Paths != 12 {
		t.Fatalf("reloaded = %#v, %v", loaded, err)
	}
	if info, err := os.Stat(filepath.Join(root, opID+".json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v, err=%v", info.Mode(), err)
	}
}

func TestStoreRequestAndResultAreIdempotent(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID, reqID := uuid.NewString(), uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "repo"), "Repo")
	first, err := store.RequestRepository(opID, reqID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RequestRepository(opID, reqID)
	if err != nil {
		t.Fatal(err)
	}
	if first.UpdatedAt != second.UpdatedAt {
		t.Fatal("idempotent request rewrote operation")
	}
	r := result(t, opID, reqID, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: "repo", RepoURL: "svn://example/repo"})
	first, err = store.ApplyRepositoryResult(r)
	if err != nil {
		t.Fatal(err)
	}
	second, err = store.ApplyRepositoryResult(r)
	if err != nil {
		t.Fatal(err)
	}
	if first.UpdatedAt != second.UpdatedAt {
		t.Fatal("idempotent result rewrote operation")
	}
	r.Result, _ = json.Marshal(control.CreateRepositoryResult{RepoID: "other", RepoURL: "svn://example/other"})
	if _, err := store.ApplyRepositoryResult(r); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting result error = %v", err)
	}
}

func TestStorePersistsFailureAndAllowsRetryWithNewRequest(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID, firstReq, retryReq := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "repo"), "Repo")
	_, _ = store.RequestRepository(opID, firstReq)
	failure := control.Result{Schema: control.Schema, OperationID: opID, RequestID: firstReq, Type: control.TicketCreateRepository, Status: control.ResultError, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Error: &control.ErrorBody{Code: "REPO_CREATE_FAILED", Message: "backend unavailable"}}
	op, err := store.ApplyRepositoryResult(failure)
	if err != nil || op.State != StateRepositoryRequestFailed {
		t.Fatalf("failure = %#v, %v", op, err)
	}
	op, err = store.RequestRepository(opID, retryReq)
	if err != nil || op.State != StateProvisioningRequested {
		t.Fatalf("retry = %#v, %v", op, err)
	}
}

func TestStoreRejectsInvalidTransitionsAndCorruptState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := newTestStore(t, root)
	opID := uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "repo"), "Repo")
	if _, err := store.StartInitialCommit(opID, uuid.NewString()); err == nil || !strings.Contains(err.Error(), "invalid provisioning transition") {
		t.Fatalf("transition error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, opID+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(opID); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("corrupt state error = %v", err)
	}
}

func TestCreateValidatedRejectsOperationIDReuseWithDifferentPath(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID := uuid.NewString()
	_, err := store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "a"), "Repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "b"), "Repo"); err == nil {
		t.Fatal("operation ID reuse accepted different path")
	}
}

func TestStoreListRestoresOperationsInDeterministicOrder(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		if _, err := store.CreateValidated(id, "client", filepath.Join(t.TempDir(), id), "Repo"); err != nil {
			t.Fatal(err)
		}
	}
	operations, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 {
		t.Fatalf("List() returned %d operations", len(operations))
	}
	if operations[0].OperationID > operations[1].OperationID {
		t.Fatalf("List() is not deterministic: %#v", operations)
	}
}

func TestInitialCommitTicketRejectsUnregisteredRequest(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID, createReq, initialReq := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "repo"), "Repo")
	_, _ = store.RequestRepository(opID, createReq)
	_, _ = store.ApplyRepositoryResult(result(t, opID, createReq, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: "repo", RepoURL: "svn://example/repo"}))
	_, _ = store.StartInitialCommit(opID, initialReq)
	_, _ = store.MarkInitialSnapshotPublished(opID, initialReq, 1, 0)
	if _, err := store.InitialCommitTicket(opID, uuid.NewString()); err == nil {
		t.Fatal("unregistered initial request accepted")
	}
}

func TestEmptyInitialSnapshotMayPublishRevisionZero(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opID, createReq, initialReq := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "repo"), "Empty")
	_, _ = store.RequestRepository(opID, createReq)
	_, _ = store.ApplyRepositoryResult(result(t, opID, createReq, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: "repo", RepoURL: "svn+ssh://_filees-data@example/repo"}))
	_, _ = store.StartInitialCommit(opID, initialReq)
	op, err := store.MarkInitialSnapshotPublished(opID, initialReq, 0, 0)
	if err != nil || op.State != StateInitialSnapshotPublished || op.Revision != 0 {
		t.Fatalf("MarkInitialSnapshotPublished = %#v, %v", op, err)
	}
	if _, err := store.InitialCommitTicket(opID, initialReq); err != nil {
		t.Fatalf("revision-zero INITIAL_COMMIT ticket rejected: %v", err)
	}
}

func TestRevisionZeroRejectsNonEmptyInitialSnapshot(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID, createReq, initialReq := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", filepath.Join(t.TempDir(), "repo"), "Non-empty")
	_, _ = store.RequestRepository(opID, createReq)
	_, _ = store.ApplyRepositoryResult(result(t, opID, createReq, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: "repo", RepoURL: "svn+ssh://_filees-data@example/repo"}))
	_, _ = store.StartInitialCommit(opID, initialReq)
	if _, err := store.MarkInitialSnapshotPublished(opID, initialReq, 0, 1); err == nil {
		t.Fatal("non-empty revision-zero snapshot accepted")
	}
}
