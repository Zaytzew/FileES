package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/config"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

func TestConfiguredRepositoryMigrationSuppressesDetachedWCOnRestart(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	repository := config.Repo{
		ID: "repo-1", ServerID: "office", RepoURL: "svn+ssh://_filees-client@example/repo-1",
		Access: "rw", LocalPath: filepath.Join(t.TempDir(), "wc"),
	}
	active, err := reconcileConfiguredRepositoryLifecycle(store, []config.Repo{repository})
	if err != nil || len(active) != 1 {
		t.Fatalf("initial migration=%+v err=%v", active, err)
	}
	records := store.List()
	if len(records) != 1 || records[0].State != localrepo.StateAttached {
		t.Fatalf("imported records=%+v", records)
	}
	if _, err := store.BeginDetach("office", "repo-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteDetach(records[0].OperationID); err != nil {
		t.Fatal(err)
	}
	active, err = reconcileConfiguredRepositoryLifecycle(store, []config.Repo{repository})
	if err != nil || len(active) != 0 || len(store.List()) != 1 {
		t.Fatalf("detached config reactivated: active=%+v records=%+v err=%v", active, store.List(), err)
	}
}

func TestRepositoryLifecycleCreateSharesCanonicalOperationWithProvisioning(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	local, _ := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	journal, _ := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	queued := ""
	service := repositoryLifecycleService{store: local, provisioning: journal, clientID: func(string) string { return "client-a" }, onCreate: func(id string) { queued = id }}
	result, err := service.BeginCreate("office", "Docs", filepath.Join(alias, "new"))
	if err != nil {
		t.Fatal(err)
	}
	op, err := journal.Get(result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if queued != result.OperationID || op.OperationID != result.OperationID || op.LocalPath != filepath.Join(real, "new") || result.LocalPath != op.LocalPath {
		t.Fatalf("result=%+v operation=%+v queued=%q", result, op, queued)
	}
}

func TestRepositoryLifecycleAllowsRetryAtSamePathAfterErroredCreate(t *testing.T) {
	local, _ := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	journal, _ := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	target := filepath.Join(t.TempDir(), "RIP")
	service := repositoryLifecycleService{store: local, provisioning: journal, clientID: func(string) string { return "client-a" }, onCreate: func(string) {}}

	first, err := service.BeginCreate("office", "RIP", target)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a later, unrelated failure (e.g. STORAGE_INSUFFICIENT reported
	// by the server) reaching terminal StateError, exactly as MarkError does
	// in BeginCreate's own error path.
	if _, err := local.MarkError(first.OperationID, errors.New("STORAGE_INSUFFICIENT: server storage requires more bytes than available")); err != nil {
		t.Fatal(err)
	}

	// A fresh attempt at the identical path must not be rejected merely
	// because a dead, terminal record still names that path.
	second, err := service.BeginCreate("office", "RIP", target)
	if err != nil {
		t.Fatalf("retry at the same path after a terminal error was rejected: %v", err)
	}
	if second.OperationID == first.OperationID {
		t.Fatal("retry must be a new operation, not the errored one")
	}
}

func TestRepositoryLifecycleResumesCreatedRepositoryAtSamePath(t *testing.T) {
	local, _ := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	journal, _ := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	target := filepath.Join(t.TempDir(), "recover")
	queued := make([]string, 0, 2)
	service := repositoryLifecycleService{
		store: local, provisioning: journal,
		clientID: func(string) string { return "client-a" },
		onCreate: func(id string) { queued = append(queued, id) },
	}
	first, err := service.BeginCreate("office", "Docs", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.MarkRepositoryCreated(first.OperationID, "repo-1", "svn+ssh://_filees-data@example/repo-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := local.MarkError(first.OperationID, errors.New("INITIAL_IMPORT_FAILED: interrupted")); err != nil {
		t.Fatal(err)
	}
	second, err := service.BeginCreate("office", "Different ignored name", target)
	if err != nil {
		t.Fatal(err)
	}
	if second.OperationID != first.OperationID || second.RepoID != "repo-1" || second.State != string(localrepo.StateRepositoryCreated) {
		t.Fatalf("resume created a new operation: first=%+v second=%+v", first, second)
	}
	if len(queued) != 2 || queued[1] != first.OperationID {
		t.Fatalf("queued=%v", queued)
	}
	if records := local.List(); len(records) != 1 || records[0].LastError != "" {
		t.Fatalf("local records after resume=%+v", records)
	}
}

func TestRepositoryLifecycleDoesNotQueueRejectedCreate(t *testing.T) {
	local, _ := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	journal, _ := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	queued := false
	root := t.TempDir()
	service := repositoryLifecycleService{store: local, provisioning: journal, clientID: func(string) string { return "client-a" }, existingRoots: []string{root}, onCreate: func(string) { queued = true }}
	if _, err := service.BeginCreate("office", "Nested", filepath.Join(root, "child")); err == nil {
		t.Fatal("overlapping create accepted")
	}
	if queued || len(local.List()) != 0 {
		t.Fatalf("rejected operation was persisted or queued: queued=%v records=%d", queued, len(local.List()))
	}
}

func TestRepositoryLifecycleLocateAcceptsOnlyExistingWorkingCopy(t *testing.T) {
	local, _ := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	journal, _ := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	oldPath := filepath.Join(t.TempDir(), "old")
	record, _ := local.BeginAttach("office", "repo-1", oldPath, false)
	_, _ = local.ApproveAttach(record.OperationID, "office", "repo-1", "svn+ssh://_filees-client@example/repo-1", "rw")
	_, _ = local.MarkAttached(record.OperationID, "repo-1")
	queued := ""
	service := repositoryLifecycleService{store: local, provisioning: journal, onRelocate: func(id string) { queued = id }}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BeginLocate("office", "repo-1", plain); err == nil {
		t.Fatal("plain directory accepted as moved working copy")
	}
	if queued != "" {
		t.Fatal("rejected locate was queued")
	}
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.MkdirAll(filepath.Join(moved, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := service.BeginLocate("office", "repo-1", moved)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := local.Get(record.OperationID)
	if queued != record.OperationID || result.PendingLocalPath != moved || !stored.RelocationAdoptExisting {
		t.Fatalf("result=%+v stored=%+v queued=%q", result, stored, queued)
	}
}
