package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

type attachmentSVNStub struct {
	url      string
	checkout int
	status   []client.StatusEntry
}

func (stub *attachmentSVNStub) Checkout(_ context.Context, url, path string) (string, error) {
	stub.checkout++
	stub.url = url
	return "", os.MkdirAll(filepath.Join(path, ".svn"), 0o700)
}
func (stub *attachmentSVNStub) GetInfo(context.Context, string) (string, error) {
	return "Path: .\nURL: " + stub.url + "\n", nil
}
func (stub *attachmentSVNStub) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	return stub.status, nil
}

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

func TestDaemonProvisionerChecksOutApprovedSharedRepository(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	repoID, url := uuid.NewString(), "svn+ssh://_filees-client@example/shared"
	record, err := local.BeginAttach("office", repoID, filepath.Join(t.TempDir(), "wc"), false)
	if err != nil {
		t.Fatal(err)
	}
	record, err = local.ApproveAttach(record.OperationID, "office", repoID, url, "r")
	if err != nil {
		t.Fatal(err)
	}
	attachments := make(chan provisionedAttachment, 1)
	stub := &attachmentSVNStub{}
	profile := clientprofile.Profile{ServerID: "office", DisplayName: "Office", IdentityFile: "/identity", KnownHosts: "/known"}
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = attachments
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	provisioner.runOne(context.Background(), record.OperationID)

	got, ok := local.Get(record.OperationID)
	if !ok || got.State != localrepo.StateAttached || stub.checkout != 1 {
		t.Fatalf("record=%+v checkout=%d", got, stub.checkout)
	}
	select {
	case attachment := <-attachments:
		if attachment.Repo.Access != "r" || attachment.Repo.RepoURL != url || attachment.Repo.LocalPath != record.LocalPath {
			t.Fatalf("attachment=%+v", attachment)
		}
	default:
		t.Fatal("runtime attachment was not published")
	}
}

func TestDaemonProvisionerRejectsMismatchedWorkingCopyURL(t *testing.T) {
	local, _ := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	journal, _ := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	repoID := uuid.NewString()
	record, _ := local.BeginAttach("office", repoID, filepath.Join(t.TempDir(), "wc"), false)
	record, _ = local.ApproveAttach(record.OperationID, "office", repoID, "svn+ssh://_filees-client@example/shared", "rw")
	stub := &attachmentSVNStub{url: "svn+ssh://_filees-client@example/other"}
	profile := clientprofile.Profile{ServerID: "office"}
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN {
		return &fixedInfoAttachmentSVN{attachmentSVNStub: stub}
	}
	provisioner.runOne(context.Background(), record.OperationID)
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateError {
		t.Fatalf("mismatched WC state=%s", got.State)
	}
}

type fixedInfoAttachmentSVN struct{ attachmentSVNStub *attachmentSVNStub }

func (stub *fixedInfoAttachmentSVN) Checkout(ctx context.Context, url, path string) (string, error) {
	return stub.attachmentSVNStub.Checkout(ctx, url, path)
}
func (stub *fixedInfoAttachmentSVN) GetInfo(context.Context, string) (string, error) {
	return "URL: svn+ssh://_filees-client@example/other\n", nil
}
func (stub *fixedInfoAttachmentSVN) Status(ctx context.Context, root string, paths []string) ([]client.StatusEntry, error) {
	return stub.attachmentSVNStub.Status(ctx, root, paths)
}
