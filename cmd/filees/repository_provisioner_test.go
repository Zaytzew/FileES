package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

	attachments := make(chan provisionedAttachment, 2)
	profile := clientprofile.Profile{ServerID: "office", DisplayName: "Office", Address: "127.0.0.1:2222", SSHPort: 2222, ClientID: clientID, IdentityFile: "/identity", KnownHosts: "/known"}
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = attachments
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); provisioner.Run(ctx) }()
	select {
	case attachment := <-attachments:
		if attachment.Repo.ID != repoID || attachment.Repo.LocalPath != wc || attachment.Repo.ServerID != "office" || attachment.Repo.SSHHostName != profile.Address || attachment.Repo.SSHPort != profile.SSHPort {
			cancel()
			t.Fatalf("restored attachment = %+v", attachment)
		}
		select {
		case duplicate := <-attachments:
			cancel()
			t.Fatalf("active provisioning attachment was published twice: %+v", duplicate)
		case <-time.After(20 * time.Millisecond):
		}
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("active attachment was not restored")
	}
	<-done
}

func TestDaemonProvisionerReconcilesRepositoryCreatedBoundary(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	opID, createID := uuid.NewString(), uuid.NewString()
	record, err := local.BeginCreateOperation(opID, "office", "Docs", filepath.Join(t.TempDir(), "wc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.CreateValidated(opID, uuid.NewString(), record.LocalPath, record.DisplayName); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.RequestRepository(opID, createID); err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	repoURL := "svn+ssh://_filees-data@example/" + repoID
	payload, _ := json.Marshal(control.CreateRepositoryResult{RepoID: repoID, RepoURL: repoURL})
	operation, err := journal.ApplyRepositoryResult(control.Result{
		Schema: control.Schema, OperationID: opID, RequestID: createID,
		Type: control.TicketCreateRepository, Status: control.ResultOK,
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: payload,
	})
	if err != nil {
		t.Fatal(err)
	}

	provisioner := newDaemonProvisioner(local, journal, nil)
	if err := provisioner.reconcileLocalBoundary(operation); err != nil {
		t.Fatal(err)
	}
	got, ok := local.Get(opID)
	if !ok || got.State != localrepo.StateRepositoryCreated || got.RepoID != repoID || got.RepoURL != repoURL || got.Access != "rw" {
		t.Fatalf("reconciled local boundary=%+v found=%v", got, ok)
	}
}

func TestDaemonProvisionerLeavesFailedRepositoryCreationForExplicitRetry(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := local.BeginCreateOperation(uuid.NewString(), "office", "Docs", filepath.Join(t.TempDir(), "wc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.MarkRepositoryCreated(record.OperationID, uuid.NewString(), "svn+ssh://_filees-data@example/repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := local.MarkError(record.OperationID, errors.New("initial import requires user recovery")); err != nil {
		t.Fatal(err)
	}

	p := newDaemonProvisioner(local, nil, nil)
	p.runOne(context.Background(), record.OperationID)
	got, ok := local.Get(record.OperationID)
	if !ok || got.State != localrepo.StateRepositoryCreated || got.LastError == "" {
		t.Fatalf("failed creation was replayed or lost: %+v", got)
	}
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

func TestDaemonProvisionerResumesAttachmentAfterCheckoutBoundary(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, _ := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	wc := filepath.Join(t.TempDir(), "wc")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	repoID, url := uuid.NewString(), "svn+ssh://_filees-client@example/shared"
	// The durable state still says attaching while .svn already exists: this is
	// the exact post-checkout/pre-publication crash boundary.
	record, _ := local.BeginAttach("office", repoID, wc, false)
	record, _ = local.ApproveAttach(record.OperationID, "office", repoID, url, "r")
	stub := &attachmentSVNStub{url: url}
	profile := clientprofile.Profile{ServerID: "office"}
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = make(chan provisionedAttachment, 1)
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	provisioner.runOne(context.Background(), record.OperationID)
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateAttached || stub.checkout != 1 {
		t.Fatalf("resumed record=%+v checkout=%d", got, stub.checkout)
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

func TestDaemonProvisionerRelocatesOnlyAfterQuiesce(t *testing.T) {
	local, journal, profile, record := relocationFixture(t)
	stub := &attachmentSVNStub{}
	events := make(chan provisionedAttachment, 2)
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	done := make(chan struct{})
	go func() {
		first := <-events
		if !first.Quiesce {
			t.Errorf("first relocation event is not quiesce: %+v", first)
		} else {
			first.Result <- nil
		}
		close(done)
	}()
	provisioner.runOne(context.Background(), record.OperationID)
	<-done
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateAttached || got.LocalPath != record.PendingLocalPath || got.PendingLocalPath != "" {
		t.Fatalf("relocated record=%+v", got)
	}
	final := <-events
	if final.Quiesce || final.Repo.LocalPath != got.LocalPath {
		t.Fatalf("final attachment=%+v", final)
	}
}

func TestDaemonProvisionerRelocationRollbackRestoresOldRuntime(t *testing.T) {
	local, journal, profile, record := relocationFixture(t)
	stub := &attachmentSVNStub{status: []client.StatusEntry{{Path: "broken", Item: "missing"}}}
	events := make(chan provisionedAttachment, 2)
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	go func() {
		quiesce := <-events
		quiesce.Result <- nil
	}()
	provisioner.runOne(context.Background(), record.OperationID)
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateAttached || got.LocalPath != record.LocalPath || got.PendingLocalPath != "" || got.LastError == "" {
		t.Fatalf("rollback record=%+v", got)
	}
	restored := <-events
	if restored.Quiesce || restored.Repo.LocalPath != record.LocalPath {
		t.Fatalf("restored attachment=%+v", restored)
	}
}

func TestDaemonProvisionerDoesNotCheckoutWhenRelocationQuiesceFails(t *testing.T) {
	local, journal, profile, record := relocationFixture(t)
	stub := &attachmentSVNStub{}
	events := make(chan provisionedAttachment, 2)
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	go func() {
		quiesce := <-events
		quiesce.Result <- errors.New("writer did not stop")
	}()
	provisioner.runOne(context.Background(), record.OperationID)
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateAttached || got.LocalPath != record.LocalPath || stub.checkout != 0 {
		t.Fatalf("record=%+v checkout=%d", got, stub.checkout)
	}
	restored := <-events
	if restored.Repo.LocalPath != record.LocalPath {
		t.Fatalf("restored runtime=%+v", restored)
	}
}

func TestDaemonProvisionerRestartPublishesOldRuntimeBeforeRelocation(t *testing.T) {
	local, journal, profile, record := relocationFixture(t)
	stub := &attachmentSVNStub{}
	events := make(chan provisionedAttachment, 4)
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); provisioner.Run(ctx) }()
	oldRuntime := <-events
	if oldRuntime.Quiesce || oldRuntime.Repo.LocalPath != record.LocalPath {
		cancel()
		t.Fatalf("first restart event=%+v", oldRuntime)
	}
	quiesce := <-events
	if !quiesce.Quiesce {
		cancel()
		t.Fatalf("second restart event=%+v", quiesce)
	}
	quiesce.Result <- nil
	newRuntime := <-events
	if newRuntime.Quiesce || newRuntime.Repo.LocalPath != record.PendingLocalPath {
		cancel()
		t.Fatalf("final restart event=%+v", newRuntime)
	}
	cancel()
	<-done
}

func reconcileFixture(t *testing.T) (*localrepo.Store, *provisioning.Store, clientprofile.Profile, localrepo.Record) {
	t.Helper()
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(t.TempDir(), "wc")
	record, _ := local.BeginAttach("office", uuid.NewString(), wc, false)
	record, _ = local.ApproveAttach(record.OperationID, "office", record.RepoID, "svn+ssh://_filees-client@example/shared", "rw")
	record, _ = local.MarkAttached(record.OperationID, record.RepoID)
	record, err = local.BeginReconcile("office", record.RepoID, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	return local, journal, clientprofile.Profile{ServerID: "office", DisplayName: "Office"}, record
}

// TestDaemonProvisionerReconcileRollsBackWhenQuiesceFails mirrors
// TestDaemonProvisionerDoesNotCheckoutWhenRelocationQuiesceFails: a failed
// quiesce must never reach the network ticket exchange or touch the WC.
func TestDaemonProvisionerReconcileRollsBackWhenQuiesceFails(t *testing.T) {
	local, journal, profile, record := reconcileFixture(t)
	stub := &attachmentSVNStub{}
	events := make(chan provisionedAttachment, 2)
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	go func() {
		quiesce := <-events
		quiesce.Result <- errors.New("writer did not stop")
	}()
	provisioner.runOne(context.Background(), record.OperationID)
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateAttached || got.LocalPath != record.LocalPath || got.ReconcileOperationID != "" || got.LastError == "" || stub.checkout != 0 {
		t.Fatalf("record=%+v checkout=%d", got, stub.checkout)
	}
	restored := <-events
	if restored.Quiesce || restored.Repo.LocalPath != record.LocalPath {
		t.Fatalf("restored runtime=%+v", restored)
	}
}

func TestSwapReconciledWorkingCopyReplacesContentAndRemovesOld(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wc")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old-carrier.dump"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	newWC := filepath.Join(t.TempDir(), "new")
	if err := os.MkdirAll(newWC, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newWC, "reconciled.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := swapReconciledWorkingCopy(root, newWC, "op-1"); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "reconciled.txt")); err != nil {
		t.Fatalf("new content missing after swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old-carrier.dump")); !os.IsNotExist(err) {
		t.Fatalf("old content survived swap: err=%v", err)
	}
	if _, err := os.Stat(newWC); !os.IsNotExist(err) {
		t.Fatalf("temp new-content dir was not consumed by the swap: err=%v", err)
	}
	if _, err := os.Stat(root + ".filees-reconcile-op-1-old"); !os.IsNotExist(err) {
		t.Fatalf("aside directory was not cleaned up: err=%v", err)
	}
}

// TestSwapReconciledWorkingCopyRollsBackOnInstallFailure forces the second
// rename to fail (newWC does not exist) and asserts root is restored to its
// original content rather than left empty.
func TestSwapReconciledWorkingCopyRollsBackOnInstallFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wc")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingNewWC := filepath.Join(t.TempDir(), "does-not-exist")
	if err := swapReconciledWorkingCopy(root, missingNewWC, "op-2"); err == nil {
		t.Fatal("swap with a missing replacement was accepted")
	}
	data, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil || string(data) != "original" {
		t.Fatalf("original content not restored after failed swap: data=%q err=%v", data, err)
	}
}

func TestInfoHasUUID(t *testing.T) {
	info := "Path: .\nURL: svn+ssh://example/repo\nRepository Root: svn+ssh://example/repo\nRepository UUID: 11111111-1111-4111-8111-111111111111\nRevision: 5\n"
	if !infoHasUUID(info, "11111111-1111-4111-8111-111111111111") {
		t.Fatal("matching UUID not detected")
	}
	if infoHasUUID(info, "22222222-2222-4222-8222-222222222222") {
		t.Fatal("mismatched UUID accepted")
	}
	if infoHasUUID(info, "") {
		t.Fatal("empty want UUID accepted")
	}
}

func relocationFixture(t *testing.T) (*localrepo.Store, *provisioning.Store, clientprofile.Profile, localrepo.Record) {
	t.Helper()
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	oldPath, newPath := filepath.Join(t.TempDir(), "old"), filepath.Join(t.TempDir(), "new")
	record, _ := local.BeginAttach("office", uuid.NewString(), oldPath, false)
	record, _ = local.ApproveAttach(record.OperationID, "office", record.RepoID, "svn+ssh://_filees-client@example/shared", "rw")
	record, _ = local.MarkAttached(record.OperationID, record.RepoID)
	record, err = local.BeginRelocation("office", record.RepoID, newPath)
	if err != nil {
		t.Fatal(err)
	}
	return local, journal, clientprofile.Profile{ServerID: "office", DisplayName: "Office"}, record
}

func TestOpenBSDAttachmentE2E(t *testing.T) {
	profilePath := os.Getenv("FILEES_ATTACHMENT_E2E_PROFILE")
	repoID := os.Getenv("FILEES_ATTACHMENT_E2E_REPO_ID")
	repoURL := os.Getenv("FILEES_ATTACHMENT_E2E_REPO_URL")
	if profilePath == "" || repoID == "" || repoURL == "" {
		t.Skip("set FILEES_ATTACHMENT_E2E_PROFILE, FILEES_ATTACHMENT_E2E_REPO_ID and FILEES_ATTACHMENT_E2E_REPO_URL")
	}
	profile, err := clientprofile.Load(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	local, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(root, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(root, "shared-wc")
	record, err := local.BeginAttach(profile.ServerID, repoID, wc, false)
	if err != nil {
		t.Fatal(err)
	}
	record, err = local.ApproveAttach(record.OperationID, profile.ServerID, repoID, repoURL, "r")
	if err != nil {
		t.Fatal(err)
	}
	attachments := make(chan provisionedAttachment, 2)
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = attachments
	provisioner.runOne(context.Background(), record.OperationID)
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateAttached {
		t.Fatalf("attachment state=%s error=%s", got.State, got.LastError)
	}
	select {
	case attachment := <-attachments:
		if attachment.Repo.Access != "r" || attachment.Repo.ID != repoID {
			t.Fatalf("runtime attachment=%+v", attachment)
		}
	default:
		t.Fatal("runtime attachment not published")
	}
	if _, err := os.Stat(filepath.Join(wc, "e2e.txt")); err != nil {
		t.Fatalf("checked-out repository content: %v", err)
	}
	relocatedWC := filepath.Join(root, "relocated-wc")
	record, err = local.BeginRelocation(profile.ServerID, repoID, relocatedWC)
	if err != nil {
		t.Fatal(err)
	}
	quiesced := make(chan struct{})
	go func() {
		defer close(quiesced)
		event := <-attachments
		if !event.Quiesce {
			t.Errorf("relocation did not request runtime quiesce: %+v", event)
			return
		}
		event.Result <- nil
	}()
	provisioner.runOne(context.Background(), record.OperationID)
	<-quiesced
	got, _ = local.Get(record.OperationID)
	if got.State != localrepo.StateAttached || got.LocalPath != relocatedWC || got.PendingLocalPath != "" {
		t.Fatalf("relocated state=%+v", got)
	}
	relocated := <-attachments
	if relocated.Quiesce || relocated.Repo.LocalPath != relocatedWC || relocated.Repo.Access != "r" {
		t.Fatalf("relocated runtime=%+v", relocated)
	}
	if _, err := os.Stat(filepath.Join(relocatedWC, "e2e.txt")); err != nil {
		t.Fatalf("relocated repository content: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wc, "e2e.txt")); err != nil {
		t.Fatalf("old working copy was not preserved: %v", err)
	}
}

func TestOpenBSDChaosChild(t *testing.T) {
	stage := os.Getenv("FILEES_CHAOS_CHILD_STAGE")
	if stage == "" {
		t.Skip("chaos child only")
	}
	root := os.Getenv("FILEES_CHAOS_ROOT")
	profile, err := clientprofile.Load(os.Getenv("FILEES_ATTACHMENT_E2E_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	local, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	records := local.List()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	record := records[0]
	svn := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:chaos:" + stage, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address})
	switch stage {
	case "attachment_checked_out":
		if _, err := svn.Checkout(t.Context(), record.RepoURL, record.LocalPath); err != nil {
			t.Fatal(err)
		}
	case "relocation_intent":
		if _, err := local.BeginRelocation(record.ServerID, record.RepoID, os.Getenv("FILEES_CHAOS_TARGET")); err != nil {
			t.Fatal(err)
		}
	case "relocation_switched":
		target := os.Getenv("FILEES_CHAOS_TARGET")
		record, err = local.BeginRelocation(record.ServerID, record.RepoID, target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svn.Checkout(t.Context(), record.RepoURL, target); err != nil {
			t.Fatal(err)
		}
		if _, err := local.CompleteRelocation(record.OperationID); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown chaos stage %q", stage)
	}
	os.Exit(91)
}

func TestOpenBSDRepositoryLifecycleChaosE2E(t *testing.T) {
	profilePath := os.Getenv("FILEES_ATTACHMENT_E2E_PROFILE")
	repoID := os.Getenv("FILEES_ATTACHMENT_E2E_REPO_ID")
	repoURL := os.Getenv("FILEES_ATTACHMENT_E2E_REPO_URL")
	if profilePath == "" || repoID == "" || repoURL == "" {
		t.Skip("set attachment E2E environment")
	}
	profile, err := clientprofile.Load(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	local, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(root, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	firstWC := filepath.Join(root, "wc-1")
	record, _ := local.BeginAttach(profile.ServerID, repoID, firstWC, false)
	record, _ = local.ApproveAttach(record.OperationID, profile.ServerID, repoID, repoURL, "r")
	runChaosChild(t, root, profilePath, "attachment_checked_out", "")

	events := make(chan provisionedAttachment, 4)
	provisioner := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	provisioner.runOne(t.Context(), record.OperationID)
	if got, _ := local.Get(record.OperationID); got.State != localrepo.StateAttached {
		t.Fatalf("post-checkout recovery=%+v", got)
	}
	<-events

	secondWC := filepath.Join(root, "wc-2")
	runChaosChild(t, root, profilePath, "relocation_intent", secondWC)
	local, _ = localrepo.Open(filepath.Join(root, "lifecycle.json"))
	provisioner = newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); provisioner.Run(ctx) }()
	oldRuntime := <-events
	quiesce := <-events
	if oldRuntime.Quiesce || !quiesce.Quiesce || oldRuntime.Repo.LocalPath != firstWC {
		cancel()
		t.Fatalf("relocation recovery order old=%+v quiesce=%+v", oldRuntime, quiesce)
	}
	quiesce.Result <- nil
	newRuntime := <-events
	if newRuntime.Repo.LocalPath != secondWC {
		cancel()
		t.Fatalf("relocated runtime=%+v", newRuntime)
	}
	cancel()
	<-done

	thirdWC := filepath.Join(root, "wc-3")
	runChaosChild(t, root, profilePath, "relocation_switched", thirdWC)
	local, _ = localrepo.Open(filepath.Join(root, "lifecycle.json"))
	provisioner = newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	provisioner.attachments = events
	provisioner.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN {
		t.Fatal("restart after atomic switch attempted network")
		return nil
	}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { defer close(done); provisioner.Run(ctx) }()
	restored := <-events
	if restored.Quiesce || restored.Repo.LocalPath != thirdWC {
		cancel()
		t.Fatalf("post-switch runtime=%+v", restored)
	}
	cancel()
	<-done
	for _, wc := range []string{firstWC, secondWC, thirdWC} {
		if _, err := os.Stat(filepath.Join(wc, "e2e.txt")); err != nil {
			t.Fatalf("preserved WC %s: %v", wc, err)
		}
	}
}

func runChaosChild(t *testing.T, root, profilePath, stage, target string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenBSDChaosChild$")
	cmd.Env = append(os.Environ(), "FILEES_CHAOS_CHILD_STAGE="+stage, "FILEES_CHAOS_ROOT="+root, "FILEES_CHAOS_TARGET="+target, "FILEES_ATTACHMENT_E2E_PROFILE="+profilePath)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 91 {
		t.Fatalf("chaos child %s did not stop at boundary: %v", stage, err)
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
