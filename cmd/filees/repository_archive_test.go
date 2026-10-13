package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/config"
	control "filees/pkg/control/v1"
	"filees/pkg/localrepo"
	"filees/pkg/passport"
	"filees/pkg/provisioning"
	"filees/pkg/talk"
	"github.com/google/uuid"
)

func archiveBoundaryFixture(t *testing.T) (*daemonProvisioner, provisioning.Operation, clientprofile.Profile) {
	t.Helper()
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(t.TempDir(), "archive.dmp"), filepath.Join(t.TempDir(), "Project")
	if err := os.WriteFile(source, []byte("SVN-fs-dump-format-version: 2\n\nfixture"), 0600); err != nil {
		t.Fatal(err)
	}
	id, clientID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := local.BeginCreateOperation(id, "office", "Project", target); err != nil {
		t.Fatal(err)
	}
	op, err := journal.CreateValidatedArchive(id, clientID, target, "Project", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.PrepareArchive(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	_, _ = journal.RequestStoragePreflight(id, uuid.NewString())
	// Use existing durable ticket/result transitions, not test writes to state.
	for _, step := range []struct {
		typ     control.TicketType
		payload any
	}{
		{control.TicketStoragePreflight, control.StoragePreflightResult{AvailableBytes: 1 << 30, RequiredBytes: 100}},
		{control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: repoID, RepoURL: "svn+ssh://example/" + repoID}},
		{control.TicketInitialCommit, control.InitialCommitResult{Acknowledged: true}},
	} {
		op, _ = journal.Get(id)
		requestID := ""
		for key, request := range op.Requests {
			if request.Type == step.typ {
				requestID = key
			}
		}
		if step.typ == control.TicketCreateRepository {
			requestID = uuid.NewString()
			_, err = journal.RequestRepository(id, requestID)
		}
		if step.typ == control.TicketInitialCommit {
			requestID = uuid.NewString()
			_, err = journal.StartInitialCommit(id, requestID)
			if err == nil {
				_, err = journal.MarkInitialSnapshotPublished(id, requestID, 1, 1)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(step.payload)
		result := control.Result{Schema: control.Schema, OperationID: id, RequestID: requestID, Type: step.typ, Status: control.ResultOK, Result: raw, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		switch step.typ {
		case control.TicketStoragePreflight:
			_, err = journal.ApplyStoragePreflightResult(result)
		case control.TicketCreateRepository:
			_, err = journal.ApplyRepositoryResult(result)
		case control.TicketInitialCommit:
			_, err = journal.ApplyInitialCommitResult(result)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	op, _ = journal.Get(id)
	profile := clientprofile.Profile{ServerID: "office", ClientID: clientID}
	p := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	if err := p.reconcileLocalBoundary(op); err != nil {
		t.Fatal(err)
	}
	return p, op, profile
}

type archiveAttachment struct {
	attachmentSVNStub
	uuid string
}

func (s *archiveAttachment) GetInfo(context.Context, string) (string, error) {
	return "URL: " + s.url + "\nRepository UUID: " + s.uuid + "\n", nil
}

func TestArchiveCarrierNeverAttachesBeforeGenerationCheckout(t *testing.T) {
	p, op, profile := archiveBoundaryFixture(t)
	record, _ := p.local.Get(op.OperationID)
	if record.State != localrepo.StateRepositoryCreated {
		t.Fatal("carrier attached at startup")
	}
	ch := make(chan provisionedAttachment, 1)
	p.attachments = ch
	p.publishAttachment(t.Context(), op)
	select {
	case <-ch:
		t.Fatal("ordinary writer received carrier")
	default:
	}
	svn := &archiveAttachment{uuid: uuid.NewString()}
	p.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return svn }
	var loadIDs []string
	load := func(_ context.Context, r localrepo.Record, _ clientprofile.Profile) (control.LoadRepositoryDumpResult, error) {
		loadIDs = append(loadIDs, r.ReconcileOperationID)
		if len(loadIDs) == 1 {
			return control.LoadRepositoryDumpResult{}, errors.New("reply lost after load")
		}
		return control.LoadRepositoryDumpResult{RepoID: op.RepoID, NewUUID: svn.uuid}, nil
	}
	if err := p.finishArchiveCreationWithLoad(t.Context(), op, profile, load); err == nil {
		t.Fatal("lost reply not exercised")
	}
	if err := p.finishArchiveCreationWithLoad(t.Context(), op, profile, load); err != nil {
		t.Fatal(err)
	}
	if len(loadIDs) != 2 || loadIDs[0] != loadIDs[1] || loadIDs[0] == op.OperationID {
		t.Fatal("load identity changed on restart")
	}
	op, _ = p.provisioning.Get(op.OperationID)
	if op.ArchivePending() {
		t.Fatal("archive completion not persisted")
	}
	if err := p.reconcileLocalBoundary(op); err != nil {
		t.Fatal(err)
	}
	p.publishAttachment(t.Context(), op)
	if attached := <-ch; attached.Repo.LocalPath != op.Archive.Target {
		t.Fatal("staging published instead of selected target")
	}
}

func TestArchiveCreateIntentRefusesNonemptyTargetBeforeEnqueue(t *testing.T) {
	p, op, _ := archiveBoundaryFixture(t)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "work"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	queued := false
	service := repositoryLifecycleService{store: p.local, provisioning: p.provisioning, clientID: p.ClientID, onCreate: func(string) { queued = true }}
	before := len(p.local.List())
	if _, err := service.BeginCreateArchive("office", "Other", target, op.Archive.Source); err == nil {
		t.Fatal("nonempty archive target accepted")
	}
	if queued || len(p.local.List()) != before {
		t.Fatal("refused input changed lifecycle")
	}
}

func TestArchiveChangedTargetPreservesFilesAndLoadedReceipt(t *testing.T) {
	p, op, profile := archiveBoundaryFixture(t)
	if err := os.MkdirAll(op.Archive.Target, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(op.Archive.Target, "new-work.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	u := uuid.NewString()
	op, err := p.provisioning.MarkArchiveLoaded(op.OperationID, u)
	if err != nil {
		t.Fatal(err)
	}
	svn := &archiveAttachment{uuid: u}
	p.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return svn }
	load := func(context.Context, localrepo.Record, clientprofile.Profile) (control.LoadRepositoryDumpResult, error) {
		t.Fatal("confirmed load repeated")
		return control.LoadRepositoryDumpResult{}, nil
	}
	if err := p.finishArchiveCreationWithLoad(t.Context(), op, profile, load); err == nil {
		t.Fatal("occupied target replaced")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "keep me" || svn.checkout != 0 {
		t.Fatal("user work changed")
	}
	op, _ = p.provisioning.Get(op.OperationID)
	if !op.ArchivePending() || op.Archive.LoadedUUID != u {
		t.Fatal("load receipt lost on target refusal")
	}
}

func TestArchiveLoadRefusesActivationRebinding(t *testing.T) {
	p, op, profile := archiveBoundaryFixture(t)
	profile.ClientID = uuid.NewString()
	load := func(context.Context, localrepo.Record, clientprofile.Profile) (control.LoadRepositoryDumpResult, error) {
		t.Fatal("rebound activation reached load")
		return control.LoadRepositoryDumpResult{}, nil
	}
	if err := p.finishArchiveCreationWithLoad(t.Context(), op, profile, load); err == nil {
		t.Fatal("rebound activation accepted")
	}
}

func TestArchiveShutdownKeepsAutomaticRestorePending(t *testing.T) {
	p, op, _ := archiveBoundaryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p.recordCreateFailureContext(ctx, op.OperationID, fmt.Errorf("checkout interrupted: %w", ctx.Err()))
	record, _ := p.local.Get(op.OperationID)
	if record.LastError != "" || record.State != localrepo.StateRepositoryCreated {
		t.Fatal("shutdown required manual retry")
	}
	attachments := make(chan provisionedAttachment, 1)
	p.attachments = attachments
	if !p.restoreOperations(t.Context()) {
		t.Fatal("restore refused")
	}
	select {
	case id := <-p.queue:
		if id != op.OperationID {
			t.Fatal("restore changed operation")
		}
	default:
		t.Fatal("pending import not queued after restart")
	}
	select {
	case <-attachments:
		t.Fatal("pending carrier attached after restart")
	default:
	}
	p.recordCreateFailureContext(ctx, op.OperationID, errors.New("real independent disk failure"))
	record, _ = p.local.Get(op.OperationID)
	if record.LastError == "" {
		t.Fatal("real failure hidden by shutdown")
	}
}

func TestArchiveCheckoutNativeSVNGenerationAndResume(t *testing.T) {
	svnadmin := filepath.Join("C:", "Program Files", "TortoiseSVN", "bin", "svnadmin.exe")
	if _, err := os.Stat(svnadmin); err != nil {
		t.Skip("native svnadmin unavailable")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command(svnadmin, "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	url := "file:///" + filepath.ToSlash(repo)
	svn := client.New(client.Options{SvnPath: "svn"})
	seed := filepath.Join(t.TempDir(), "seed")
	if _, err := svn.Checkout(t.Context(), url, seed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"drawing.dwg", "photo.jpg"} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte("synthetic archive content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svn.Add(t.Context(), seed, []string{"drawing.dwg", "photo.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svn.PropSet(t.Context(), seed, "svn:needs-lock", "true", []string{"drawing.dwg", "photo.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svn.PropSet(t.Context(), seed, passport.AppendOnlyProperty, "true", []string{"photo.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svn.Commit(t.Context(), seed, []string{"drawing.dwg", "photo.jpg"}, "fixture archived barriers"); err != nil {
		t.Fatal(err)
	}
	info, err := svn.GetInfo(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	// Obtain the real UUID through the native CLI, not a made-up receipt.
	var u string
	for _, line := range strings.Split(info, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Repository UUID:"); ok {
			u = strings.TrimSpace(value)
		}
	}
	op := provisioning.Operation{OperationID: uuid.NewString(), RepoURL: url, Archive: &provisioning.ArchiveImport{Target: filepath.Join(t.TempDir(), "Project"), LoadedUUID: u}}
	if err := checkoutArchiveTarget(t.Context(), op, svn, nil); err != nil {
		t.Fatal(err)
	}
	if err := checkoutArchiveTarget(t.Context(), op, svn, nil); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(op.Archive.Target, ".filees", "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if applyEditingPolicyMigration(t.Context(), config.Repo{ID: "fixture"}, svn, op.Archive.Target, stateDir, "fixture", false, nil, talk.With("fixture")) {
		t.Fatal("native orphan sanitation deferred")
	}
	props, err := svn.PropList(t.Context(), op.Archive.Target, "svn:needs-lock")
	if err != nil || props["drawing.dwg"] || !props["photo.jpg"] {
		t.Fatalf("native props=%v err=%v", props, err)
	}
	for _, name := range []string{"drawing.dwg", "photo.jpg"} {
		info, err := os.Stat(filepath.Join(op.Archive.Target, name))
		if err != nil {
			t.Fatal(err)
		}
		if (info.Mode().Perm()&0200 != 0) != (name == "drawing.dwg") {
			t.Fatalf("wrong write access: %s %v", name, info.Mode())
		}
	}
	path := filepath.Join(op.Archive.Target, "local-work.txt")
	if err := os.WriteFile(path, []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkoutArchiveTarget(t.Context(), op, svn, nil); err == nil {
		t.Fatal("dirty native target accepted")
	}
}
