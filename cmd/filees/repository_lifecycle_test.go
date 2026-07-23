package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

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
