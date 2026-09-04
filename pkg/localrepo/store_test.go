package localrepo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDeletionPersistsServerBoundaryRecoveryAndLocalCleanupSeparately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(t.TempDir(), "wc")
	repoID := uuid.NewString()
	record, _, err := store.EnsureConfiguredAttached("spot", repoID, "svn+ssh://_filees-client@example/"+repoID, "rw", wc, "Archiwum")
	if err != nil {
		t.Fatal(err)
	}
	deleting, err := store.BeginDetach("spot", repoID, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
	deleting, err = store.MarkServerDeleted(deleting.OperationID, deadline)
	if err != nil || !deleting.ServerDeleteCompleted || deleting.State != StateDeleting {
		t.Fatalf("server boundary=%+v err=%v", deleting, err)
	}
	kit := filepath.Join(t.TempDir(), "recovery.fkr")
	deleting, err = store.MarkRecoveryPrepared(deleting.OperationID, kit)
	if err != nil || !deleting.RecoveryPrepared || deleting.RecoveryKitPath != kit {
		t.Fatalf("recovery boundary=%+v err=%v", deleting, err)
	}
	if _, err := store.RecordDetachError(record.OperationID, errors.New("wc.db is in use")); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.Get(record.OperationID)
	if !ok || !persisted.ServerDeleteCompleted || !persisted.RecoveryPrepared || persisted.LastError == "" || persisted.State != StateDeleting {
		t.Fatalf("persisted cleanup-pending deletion=%+v", persisted)
	}
	if _, err := reopened.MarkLocalCleanupCompleted(record.OperationID); err != nil {
		t.Fatal(err)
	}
	completed, err := reopened.CompleteDetach(record.OperationID)
	if err != nil || completed.State != StateDeleted || completed.RetainUntil == "" || completed.RecoveryKitPath != kit {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestRemoteRepositoryDeletionHasNoFabricatedWorkingCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	deleting, err := store.BeginDelete("spot", "repo-remote", "Zdalne archiwum")
	if err != nil {
		t.Fatal(err)
	}
	if deleting.State != StateDeleting || deleting.LocalPath != "" || !deleting.DeleteRepository || deleting.LocalCleanupCompleted {
		t.Fatalf("remote delete intent=%+v", deleting)
	}
	if _, err := store.MarkLocalCleanupCompleted(deleting.OperationID); err == nil {
		t.Fatal("remote cleanup crossed server deletion boundary")
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	deleting, ok := reopened.Get(deleting.OperationID)
	if !ok || deleting.LocalPath != "" || deleting.DisplayName != "Zdalne archiwum" {
		t.Fatalf("persisted remote delete=%+v found=%v", deleting, ok)
	}
	deadline := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := reopened.MarkServerDeleted(deleting.OperationID, deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.MarkLocalCleanupCompleted(deleting.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.MarkRecoveryPrepared(deleting.OperationID, ""); err != nil {
		t.Fatal(err)
	}
	completed, err := reopened.CompleteDetach(deleting.OperationID)
	if err != nil || completed.State != StateDeleted || !completed.LocalCleanupCompleted {
		t.Fatalf("completed remote delete=%+v err=%v", completed, err)
	}
}

func TestOpenMigratesHistoricalDeletedTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := store.EnsureConfiguredAttached("spot", "repo-old", "svn+ssh://example/repo-old", "rw", filepath.Join(t.TempDir(), "wc"), "Old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDetach("spot", "repo-old", true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := store.MarkServerDeleted(record.OperationID, deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRecoveryPrepared(record.OperationID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkLocalCleanupCompleted(record.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteDetach(record.OperationID); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var old document
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old.Records[0].ServerDeleteCompleted = false
	old.Records[0].RetainUntil = ""
	old.Records[0].RecoveryPrepared = false
	old.Records[0].LocalCleanupCompleted = false
	raw, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	migrated, ok := reopened.Get(record.OperationID)
	if !ok || !migrated.ServerDeleteCompleted || !migrated.RecoveryPrepared || !migrated.LocalCleanupCompleted || migrated.RecoveryKitPath != "" {
		t.Fatalf("historical tombstone was not migrated: %+v", migrated)
	}
}

func TestStorePersistsCreateAndAttachIntents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.BeginCreate("primary", "Dokumenty", filepath.Join(t.TempDir(), "docs"))
	if err != nil {
		t.Fatal(err)
	}
	attached, err := s.BeginAttach("primary", "repo-1", filepath.Join(t.TempDir(), "share"), true)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != StateRequestPending || attached.State != StatePolicyPending {
		t.Fatalf("unexpected states: %s %s", created.State, attached.State)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.List()); got != 2 {
		t.Fatalf("records=%d", got)
	}
}

func TestConfiguredAttachmentIsImportedOnceAndDetachTombstoneWins(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(t.TempDir(), "wc")
	url := "svn+ssh://_filees-client@example/repo-1"
	imported, created, err := store.EnsureConfiguredAttached("primary", "repo-1", url, "rw", wc, "Repo")
	if err != nil || !created || imported.State != StateAttached {
		t.Fatalf("imported=%+v created=%v err=%v", imported, created, err)
	}
	replay, created, err := store.EnsureConfiguredAttached("primary", "repo-1", url, "rw", wc, "Repo")
	if err != nil || created || replay.OperationID != imported.OperationID {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	if _, err := store.BeginDetach("primary", "repo-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteDetach(imported.OperationID); err != nil {
		t.Fatal(err)
	}
	tombstone, created, err := store.EnsureConfiguredAttached("primary", "repo-1", url, "rw", wc, "Repo")
	if err != nil || created || tombstone.State != StateDetached || tombstone.OperationID != imported.OperationID {
		t.Fatalf("tombstone=%+v created=%v err=%v", tombstone, created, err)
	}
}

func TestStorePersistsExplicitOperationAndTerminalStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	opID := uuid.NewString()
	if _, err := store.BeginCreateOperation(opID, "primary", "Docs", filepath.Join(t.TempDir(), "docs")); err != nil {
		t.Fatal(err)
	}
	attached, err := store.MarkAttached(opID, "repo-1")
	if err != nil || attached.State != StateAttached || attached.RepoID != "repo-1" {
		t.Fatalf("attached=%+v err=%v", attached, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(opID)
	if !ok || got.State != StateAttached || got.RepoID != "repo-1" {
		t.Fatalf("reopened=%+v found=%v", got, ok)
	}

	errorID := uuid.NewString()
	_, _ = reopened.BeginCreateOperation(errorID, "primary", "Other", filepath.Join(t.TempDir(), "other"))
	failed, err := reopened.MarkError(errorID, os.ErrPermission)
	if err != nil || failed.State != StateError || failed.LastError == "" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}

func TestStoreRequiresMatchingApprovalBeforeAttachment(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginAttach("primary", "repo-1", filepath.Join(t.TempDir(), "share"), true)
	if err != nil {
		t.Fatal(err)
	}
	url := "svn+ssh://_filees-client@example/repo"
	if _, err := store.ApproveAttach(record.OperationID, "other", "repo-1", url, "r"); err == nil {
		t.Fatal("mismatched server approval accepted")
	}
	approved, err := store.ApproveAttach(record.OperationID, "primary", "repo-1", url, "r")
	if err != nil || approved.State != StateAttaching {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	if _, err := store.ApproveAttach(record.OperationID, "primary", "repo-1", url, "r"); err != nil {
		t.Fatalf("idempotent approval failed: %v", err)
	}
}

func TestCreatedRepositoryBoundarySurvivesFailureAndResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(t.TempDir(), "docs")
	record, err := store.BeginCreate("primary", "Docs", localPath)
	if err != nil {
		t.Fatal(err)
	}
	const repoURL = "svn+ssh://_filees-data@example/repo-1"
	created, err := store.MarkRepositoryCreated(record.OperationID, "repo-1", repoURL)
	if err != nil || created.State != StateRepositoryCreated || created.Access != "rw" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	failed, err := store.MarkError(record.OperationID, os.ErrPermission)
	if err != nil || failed.State != StateRepositoryCreated || failed.LastError == "" {
		t.Fatalf("failed boundary=%+v err=%v", failed, err)
	}
	if _, err := store.BeginCreate("primary", "Duplicate", localPath); err == nil {
		t.Fatal("created server repository stopped owning its local path")
	}
	resumed, err := store.ResumeCreate(record.OperationID)
	if err != nil || resumed.State != StateRepositoryCreated || resumed.LastError != "" || resumed.RepoURL != repoURL {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(record.OperationID)
	if !ok || got.State != StateRepositoryCreated || got.RepoID != "repo-1" {
		t.Fatalf("reopened=%+v found=%v", got, ok)
	}
}

func TestRepairCreatedRepositoryInputKeepsServerIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(t.TempDir(), "��D�-KOLUMNY")
	record, err := store.BeginCreate("spot", "��D�-KOLUMNY", badPath)
	if err != nil {
		t.Fatal(err)
	}
	repoID, repoURL := "190a9c82-335b-5ee4-9960-f7f614d06b38", "svn+ssh://_filees-data@example/190a9c82-335b-5ee4-9960-f7f614d06b38"
	if _, err := store.MarkRepositoryCreated(record.OperationID, repoID, repoURL); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkError(record.OperationID, errors.New("checkout failed")); err != nil {
		t.Fatal(err)
	}
	goodPath := filepath.Join(t.TempDir(), "ŁÓDŹ-KOLUMNY")
	repaired, err := store.RepairCreatedRepositoryInput(record.OperationID, "Łódź-kolumny", goodPath)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.RepoID != repoID || repaired.RepoURL != repoURL || repaired.DisplayName != "Łódź-kolumny" || repaired.LocalPath != goodPath || repaired.LastError != "" || repaired.State != StateRepositoryCreated {
		t.Fatalf("repaired record = %+v", repaired)
	}
}

func TestStoreRelocationIsDurableAndKeepsOldPathOnFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldPath, newPath := filepath.Join(t.TempDir(), "old"), filepath.Join(t.TempDir(), "new")
	record, _ := store.BeginAttach("primary", "repo-1", oldPath, false)
	_, _ = store.ApproveAttach(record.OperationID, "primary", "repo-1", "svn+ssh://_filees-client@example/repo", "rw")
	_, _ = store.MarkAttached(record.OperationID, "repo-1")
	relocating, err := store.BeginRelocation("primary", "repo-1", newPath)
	if err != nil || relocating.State != StateRelocating || relocating.LocalPath != oldPath || relocating.PendingLocalPath != newPath {
		t.Fatalf("relocating=%+v err=%v", relocating, err)
	}
	failed, err := store.FailRelocation(record.OperationID, os.ErrPermission)
	if err != nil || failed.State != StateAttached || failed.LocalPath != oldPath || failed.PendingLocalPath != "" || failed.LastError == "" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	_, _ = store.BeginRelocation("primary", "repo-1", newPath)
	completed, err := store.CompleteRelocation(record.OperationID)
	if err != nil || completed.State != StateAttached || completed.LocalPath != newPath || completed.PendingLocalPath != "" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestStoreLocatePersistsAdoptExistingAndClearsItAtBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	oldPath, movedPath := filepath.Join(t.TempDir(), "old"), filepath.Join(t.TempDir(), "moved")
	record, _ := store.BeginAttach("primary", "repo-1", oldPath, false)
	_, _ = store.ApproveAttach(record.OperationID, "primary", "repo-1", "svn+ssh://_filees-client@example/repo", "rw")
	_, _ = store.MarkAttached(record.OperationID, "repo-1")
	locating, err := store.BeginLocate("primary", "repo-1", movedPath)
	if err != nil || !locating.RelocationAdoptExisting || locating.PendingLocalPath != movedPath {
		t.Fatalf("locating=%+v err=%v", locating, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := reopened.Get(record.OperationID)
	if !persisted.RelocationAdoptExisting {
		t.Fatalf("locate mode was not durable: %+v", persisted)
	}
	completed, err := reopened.CompleteRelocation(record.OperationID)
	if err != nil || completed.LocalPath != movedPath || completed.RelocationAdoptExisting {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestStoreLocateMayReaffirmCurrentRoot(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(t.TempDir(), "biblia")
	record, _ := store.BeginAttach("manual", "repo-1", current, false)
	_, _ = store.ApproveAttach(record.OperationID, "manual", "repo-1", "svn+ssh://_filees-client@example/repo", "rw")
	_, _ = store.MarkAttached(record.OperationID, "repo-1")
	locating, err := store.BeginLocate("manual", "repo-1", current)
	if err != nil || locating.PendingLocalPath != current || !locating.RelocationAdoptExisting {
		t.Fatalf("reaffirm locate=%+v err=%v", locating, err)
	}
	nested := filepath.Join(current, "child")
	if _, err := store.BeginLocate("manual", "repo-1", nested); err == nil {
		t.Fatal("nested path accepted as locate target")
	}
}

func TestStoreReconcileIsDurableAndKeepsPathOnFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wc")
	record, _ := store.BeginAttach("primary", "repo-1", path, false)
	_, _ = store.ApproveAttach(record.OperationID, "primary", "repo-1", "svn+ssh://_filees-client@example/repo", "rw")
	_, _ = store.MarkAttached(record.OperationID, "repo-1")

	reconciling, err := store.BeginReconcile("primary", "repo-1", true, nil)
	if err != nil || reconciling.State != StateReconciling || reconciling.LocalPath != path || reconciling.ReconcileOperationID == "" {
		t.Fatalf("reconciling=%+v err=%v", reconciling, err)
	}
	// Resuming the same in-progress attempt returns the same operation ID,
	// not a new one - the orchestration relies on this for idempotent
	// staging directory naming across a daemon restart.
	again, err := store.BeginReconcile("primary", "repo-1", true, nil)
	if err != nil || again.ReconcileOperationID != reconciling.ReconcileOperationID {
		t.Fatalf("resumed reconcile got a different operation: first=%s again=%s err=%v", reconciling.ReconcileOperationID, again.ReconcileOperationID, err)
	}

	failed, err := store.FailReconcile(record.OperationID, os.ErrPermission)
	if err != nil || failed.State != StateAttached || failed.LocalPath != path || failed.ReconcileOperationID != "" || failed.LastError == "" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}

	restarted, err := store.BeginReconcile("primary", "repo-1", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteReconcile(record.OperationID)
	if err != nil || completed.State != StateAttached || completed.LocalPath != path || completed.ReconcileOperationID != "" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	_ = restarted
	// A second CompleteReconcile call (e.g. a retried IPC confirmation) is
	// an idempotent no-op, not an error.
	replay, err := store.CompleteReconcile(record.OperationID)
	if err != nil || replay.State != StateAttached {
		t.Fatalf("idempotent replay of CompleteReconcile: replay=%+v err=%v", replay, err)
	}
}

func TestStoreRejectsDuplicateRepoAndOverlappingRoots(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "root")
	if _, err = s.BeginAttach("primary", "repo-1", root, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginAttach("primary", "repo-1", filepath.Join(t.TempDir(), "other"), false); err == nil {
		t.Fatal("duplicate repo accepted")
	}
	if _, err = s.BeginCreate("primary", "nested", filepath.Join(root, "child")); err == nil {
		t.Fatal("overlapping root accepted")
	}
}

func TestStoreFailsClosedOnUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	if err := os.WriteFile(path, []byte(`{"schema":"filees.local-repository-lifecycle/v1","records":[],"extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestStorePersistsAndCompletesLocalDetach(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginAttach("primary", "repo-1", filepath.Join(t.TempDir(), "wc"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(record.OperationID, "primary", "repo-1", "svn+ssh://example/repo-1", "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAttached(record.OperationID, "repo-1"); err != nil {
		t.Fatal(err)
	}
	detaching, err := store.BeginDetach("primary", "repo-1", false)
	if err != nil || detaching.State != StateDetaching || detaching.DeleteRepository {
		t.Fatalf("detaching=%+v err=%v", detaching, err)
	}
	if _, err := uuid.Parse(detaching.DetachOperationID); err != nil {
		t.Fatalf("detach operation ID=%q: %v", detaching.DetachOperationID, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.Get(record.OperationID)
	if !ok || persisted.State != StateDetaching || persisted.DetachOperationID != detaching.DetachOperationID {
		t.Fatalf("persisted detach=%+v found=%v", persisted, ok)
	}
	completed, err := reopened.CompleteDetach(record.OperationID)
	if err != nil || completed.State != StateDetached {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if _, err := reopened.BeginAttach("primary", "repo-1", completed.LocalPath, false); err != nil {
		t.Fatalf("terminal detach still claims repo/path: %v", err)
	}
}

func TestRepairRetriesSameDurableRepositoryOperation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	opID, repoID := uuid.NewString(), uuid.NewString()
	wc := filepath.Join(t.TempDir(), "wc")
	record, err := store.BeginCreateOperation(opID, "office", "Docs", wc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRepositoryCreated(opID, repoID, "svn+ssh://example/"+repoID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkError(opID, errors.New("initial import failed")); err != nil {
		t.Fatal(err)
	}
	retried, err := store.Retry(opID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.OperationID != record.OperationID || retried.RepoID != repoID || retried.LocalPath != wc || retried.State != StateRepositoryCreated || retried.LastError != "" {
		t.Fatalf("retry changed durable identity: %+v", retried)
	}
}

func TestRepairAbandonReleasesAttachIdentityWithoutDeletingData(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	wc := filepath.Join(t.TempDir(), "wc")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(wc, "projekt.dwg")
	if err := os.WriteFile(userFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginAttach("office", repoID, wc, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(record.OperationID, "office", repoID, "svn+ssh://example/"+repoID, "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkError(record.OperationID, errors.New("checkout failed")); err != nil {
		t.Fatal(err)
	}
	abandoned, err := store.Abandon(record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.State != StateAbandoned || abandoned.LastError != "" {
		t.Fatalf("abandoned=%+v", abandoned)
	}
	if raw, err := os.ReadFile(userFile); err != nil || string(raw) != "keep" {
		t.Fatalf("abandon changed user data: %q, %v", raw, err)
	}
	if _, err := store.BeginAttach("office", repoID, wc, false); err != nil {
		t.Fatalf("abandoned identity still blocks a new operation: %v", err)
	}
}

func TestRepairCannotAbandonForwardOnlyDetach(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	record, _, err := store.EnsureConfiguredAttached("office", repoID, "svn+ssh://example/"+repoID, "rw", filepath.Join(t.TempDir(), "wc"), "Docs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDetach("office", repoID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordDetachError(record.OperationID, errors.New("wc.db busy")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Abandon(record.OperationID); err == nil {
		t.Fatal("forward-only detach was abandoned")
	}
	retried, err := store.Retry(record.OperationID)
	if err != nil || retried.State != StateDetaching || retried.LastError != "" {
		t.Fatalf("detach retry=%+v err=%v", retried, err)
	}
}

func TestStoreDeletionRetryKeepsSameDurableOperation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.BeginAttach("primary", "repo-1", filepath.Join(t.TempDir(), "wc"), false)
	_, _ = store.ApproveAttach(record.OperationID, "primary", "repo-1", "svn+ssh://example/repo-1", "rw")
	_, _ = store.MarkAttached(record.OperationID, "repo-1")
	deleting, err := store.BeginDetach("primary", "repo-1", true)
	if err != nil || deleting.State != StateDeleting || !deleting.DeleteRepository {
		t.Fatalf("deleting=%+v err=%v", deleting, err)
	}
	failed, err := store.RecordDetachError(record.OperationID, os.ErrPermission)
	if err != nil || failed.State != StateDeleting || failed.LastError == "" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	retry, err := store.BeginDetach("primary", "repo-1", true)
	if err != nil || retry.DetachOperationID != deleting.DetachOperationID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	deadline := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := store.MarkServerDeleted(record.OperationID, deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRecoveryPrepared(record.OperationID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkLocalCleanupCompleted(record.OperationID); err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteDetach(record.OperationID)
	if err != nil || completed.State != StateDeleted || !completed.DeleteRepository {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}
