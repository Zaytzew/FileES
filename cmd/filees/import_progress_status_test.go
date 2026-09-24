package main

import (
	"path/filepath"
	"testing"

	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

// repo.lifecycle_status answers the initial publication's progress while it
// runs and nothing once the provisioner cleared it - the GUI's creation poll
// is the only route to the overlay for a repository not yet in the state.
func TestLifecycleStatusCarriesImportProgressOnlyWhileItRuns(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "repository-lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	const opID = "4a1d9e77-0d9b-4c35-9d62-0f1f1e3c7a11"
	if _, err := local.BeginCreateOperation(opID, "demo", "Projekt testowy", filepath.Join(t.TempDir(), "wc")); err != nil {
		t.Fatal(err)
	}
	provisioner := &daemonProvisioner{}
	service := repositoryLifecycleService{store: local, importProgress: provisioner.ImportProgress}

	if result, err := service.Status(opID); err != nil || result.ImportProgress != nil {
		t.Fatalf("before the import: %+v, %v", result.ImportProgress, err)
	}
	provisioner.setImportProgress(opID, provisioning.ImportProgress{FilesDone: 120, FilesTotal: 330, BytesSent: 80 << 20, BytesTotal: 227 << 20}, "2026-09-24T17:09:44Z")
	result, err := service.Status(opID)
	if err != nil || result.ImportProgress == nil || result.ImportProgress.FilesDone != 120 || result.ImportProgress.FilesTotal != 330 || result.ImportProgress.BytesTotal != 227<<20 || result.ImportProgress.StartedAt == "" {
		t.Fatalf("during the import: %+v, %v", result.ImportProgress, err)
	}
	provisioner.clearImportProgress(opID)
	if result, err := service.Status(opID); err != nil || result.ImportProgress != nil {
		t.Fatalf("after the import: %+v, %v", result.ImportProgress, err)
	}
}
