package localrepo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

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
	completed, err := store.CompleteDetach(record.OperationID)
	if err != nil || completed.State != StateDeleted || !completed.DeleteRepository {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}
