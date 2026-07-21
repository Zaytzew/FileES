package main

import (
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
