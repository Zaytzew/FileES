package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"filees/pkg/recoverykit"

	"github.com/google/uuid"
)

func TestDismissedRepositoryRecoveryIsAbsentFromRecoveryListAndDownload(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	record, _, err := local.EnsureConfiguredAttached("spot", repoID, "svn+ssh://example/"+repoID, "rw", filepath.Join(t.TempDir(), "wc"), "Archiwum")
	if err != nil {
		t.Fatal(err)
	}
	deleting, err := local.BeginDetach("spot", repoID, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	if _, err := local.MarkServerDeleted(record.OperationID, deadline.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	kitPath := filepath.Join(root, "archive.fkr")
	if _, err := local.MarkRecoveryPrepared(record.OperationID, kitPath); err != nil {
		t.Fatal(err)
	}
	if _, err := local.DismissRecovery("spot", repoID, deleting.DetachOperationID); err != nil {
		t.Fatal(err)
	}
	registry := recoverykit.Registry{Root: root}
	if err := registry.Put(recoverykit.RegistryEntry{
		Schema: recoverykit.RegistrySchema, OperationID: deleting.DetachOperationID,
		ServerID: "spot", ServerName: "Spot", KitPath: kitPath, ArchiveCount: 1,
		DownloadUntil: deadline, AdminGraceUntil: deadline.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := realmRemovalClientService{local: local, registry: registry}
	listed, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("dismissed recovery remained in aggregate list: %+v", listed)
	}
	_, err = service.Download(context.Background(), contract.RecoveryDownloadPayload{
		OperationID: deleting.DetachOperationID,
		OutputRoot:  t.TempDir(),
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dismissed recovery download error=%v, want not-exist", err)
	}
}
