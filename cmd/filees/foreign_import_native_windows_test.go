//go:build windows && native_foreign_probe

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"github.com/google/uuid"
)

func TestForeignNativeImportCanEndWithoutReadoptingFolder(t *testing.T) {
	helper := os.Getenv("FILEES_LAB_FOREIGN_HELPER")
	if helper == "" {
		t.Fatal("FILEES_LAB_FOREIGN_HELPER is required")
	}
	root := t.TempDir()
	if out, err := exec.Command(helper, "create", root).CombinedOutput(); err != nil {
		t.Fatalf("independent fixture: %v %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "remove", root).CombinedOutput(); err != nil {
			t.Errorf("unregister fixture: %v %s", err, out)
		}
	})
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	queued := 0
	service := repositoryLifecycleService{store: local, provisioning: journal, clientID: func(string) string { return "fixture" }, onCreate: func(string) { queued++ }}
	_, err = service.BeginCreate("fixture", "Fixture", root)
	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Key != errcat.KeyInitialContentUnavailable || queued != 0 || len(local.List()) != 0 {
		t.Fatalf("initial create escaped refusal: %v queued=%d records=%v", err, queued, local.List())
	}
	// Simulate an older client's durable import, with .svn already present.
	id, requestID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repoURL := "svn+ssh://_filees-client@example/" + repoID
	record, err := local.BeginCreateOperation(id, "fixture", "Fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.CreateValidated(id, "fixture", root, "Fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.RequestRepository(id, requestID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(control.CreateRepositoryResult{RepoID: repoID, RepoURL: repoURL})
	if _, err := journal.ApplyRepositoryResult(control.Result{Schema: control.Schema, OperationID: id, RequestID: requestID, Type: control.TicketCreateRepository, Status: control.ResultOK, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: payload}); err != nil {
		t.Fatal(err)
	}
	record, err = local.MarkRepositoryCreated(id, repoID, repoURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(local, journal, nil)
	// Intentionally no usable SSH profile: metadata refusal must precede it.
	p.profiles["fixture"] = clientprofile.Profile{}
	p.runOne(t.Context(), record.OperationID)
	failed, _ := local.Get(id)
	if failed.State != localrepo.StateRepositoryCreated || failed.LastErrorKey != string(errcat.KeyInitialContentUnavailable) {
		t.Fatalf("stuck replay did not terminate truthfully: %+v", failed)
	}
	_, err = p.RepairLifecycle(t.Context(), id, "retry")
	if !errors.As(err, &fault) || fault.Key != errcat.KeyInitialContentUnavailable {
		t.Fatalf("retry: %v", err)
	}
	_, err = service.BeginCreate("fixture", "Ignored", root)
	if !errors.As(err, &fault) || fault.Key != errcat.KeyInitialContentUnavailable || queued != 0 {
		t.Fatalf("resume: %v queued=%d", err, queued)
	}
	ended, err := p.RepairLifecycle(t.Context(), id, "abandon")
	if err != nil || ended.State != localrepo.StateAbandoned || ended.RepoID != repoID {
		t.Fatalf("end local attempt: %+v %v", ended, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "unavailable.dwg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".svn")); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New(shortDaemonSocket(t))
	state := server.RegisterProjectedRepo(repoID, "Fixture", repoURL, "fixture", "rw", contract.StateActive, false)
	view := clientview.View{Repositories: []clientview.Repository{{RepoID: repoID, DisplayName: "Fixture", URL: repoURL, Access: "rw", State: "active"}}}
	syncProjectionKnowledge(server, "fixture", view, nil, local)
	summary := state.Summary()
	if summary.Attached || summary.LocalPath != "" || summary.LifecycleOperationID != "" || summary.State != contract.StateUnattached {
		t.Fatalf("not remote-only: %+v", summary)
	}
	_, err = service.BeginAttach("fixture", repoID, root, false)
	if !errors.As(err, &fault) || fault.Key != errcat.KeyInitialContentUnavailable {
		t.Fatalf("bad folder reattached: %v", err)
	}
	fresh := t.TempDir()
	attached, err := service.BeginAttach("fixture", repoID, fresh, false)
	if err != nil || attached.LocalPath != fresh || attached.OperationID == id {
		t.Fatalf("fresh attachment refused: %+v %v", attached, err)
	}
}
