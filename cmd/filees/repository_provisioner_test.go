package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

func TestDaemonProvisionerRestoresActiveAttachmentWithoutNetwork(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	opID, createID, initialID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	clientID, repoID := uuid.NewString(), uuid.NewString()
	wc := filepath.Join(t.TempDir(), "wc")
	_, _ = local.BeginCreateOperation(opID, "office", "Docs", wc)
	_, _ = journal.CreateValidated(opID, clientID, wc, "Docs")
	_, _ = journal.RequestRepository(opID, createID)
	createPayload, _ := json.Marshal(control.CreateRepositoryResult{RepoID: repoID, RepoURL: "svn+ssh://_filees-data@example/" + repoID})
	_, err = journal.ApplyRepositoryResult(control.Result{Schema: control.Schema, OperationID: opID, RequestID: createID, Type: control.TicketCreateRepository, Status: control.ResultOK, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: createPayload})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = journal.StartInitialCommit(opID, initialID)
	_, _ = journal.MarkInitialSnapshotPublished(opID, initialID, 1, 1)
	initialPayload, _ := json.Marshal(control.InitialCommitResult{Acknowledged: true})
	_, err = journal.ApplyInitialCommitResult(control.Result{Schema: control.Schema, OperationID: opID, RequestID: initialID, Type: control.TicketInitialCommit, Status: control.ResultOK, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: initialPayload})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.MarkAttached(opID, repoID); err != nil {
		t.Fatal(err)
	}

	attachments := make(chan provisionedAttachment, 1)
	profile := clientprofile.Profile{ServerID: "office", DisplayName: "Office", ClientID: clientID, IdentityFile: "/identity", KnownHosts: "/known"}
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = attachments
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); provisioner.Run(ctx) }()
	select {
	case attachment := <-attachments:
		cancel()
		if attachment.Repo.ID != repoID || attachment.Repo.LocalPath != wc || attachment.Repo.ServerID != "office" {
			t.Fatalf("restored attachment = %+v", attachment)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("active attachment was not restored")
	}
	<-done
}
