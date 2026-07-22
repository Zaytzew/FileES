package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	control "filees/pkg/control/v1"
)

func TestOrchestratorRunsCreateThroughActive(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID := uuid.NewString()
	if _, err := store.CreateValidated(opID, "client", root, "Docs"); err != nil {
		t.Fatal(err)
	}
	transport := &fakeControlExchange{}
	svn := &fakeInitialSVN{root: root, items: map[string]string{}}
	orchestrator := Orchestrator{Store: store, Control: transport, SVN: svn, Limits: ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024}}
	op, err := orchestrator.RunCreate(context.Background(), opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateActive || op.Revision != 1 || len(transport.types) != 3 {
		t.Fatalf("operation=%+v tickets=%v", op, transport.types)
	}
}

func TestOrchestratorResumesAtPublishedSnapshotWithoutRecommit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID := uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", root, "Docs")
	transport := &fakeControlExchange{failInitialOnce: true}
	svn := &fakeInitialSVN{root: root, items: map[string]string{}}
	orchestrator := Orchestrator{Store: store, Control: transport, SVN: svn, Limits: ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024}}
	if _, err := orchestrator.RunCreate(context.Background(), opID); err == nil {
		t.Fatal("expected interrupted INITIAL_COMMIT exchange")
	}
	before, err := store.Get(opID)
	if err != nil || before.State != StateInitialSnapshotPublished || len(svn.commits) != 1 {
		t.Fatalf("before restart=%+v commits=%v err=%v", before, svn.commits, err)
	}
	after, err := orchestrator.RunCreate(context.Background(), opID)
	if err != nil || after.State != StateActive || len(svn.commits) != 1 {
		t.Fatalf("after restart=%+v commits=%v err=%v", after, svn.commits, err)
	}
	if len(transport.tickets) != 4 || transport.tickets[2].RequestID != transport.tickets[3].RequestID {
		t.Fatalf("INITIAL_COMMIT request ID changed across retry: %+v", transport.tickets)
	}
}

func TestOrchestratorRetriesSameCreateRequestAfterLostResponse(t *testing.T) {
	root := t.TempDir()
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	opID := uuid.NewString()
	_, _ = store.CreateValidated(opID, "client", root, "Docs")
	transport := &fakeControlExchange{failCreateOnce: true}
	orchestrator := Orchestrator{Store: store, Control: transport, SVN: &fakeInitialSVN{root: root, items: map[string]string{}}, Limits: ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024}}
	if _, err := orchestrator.RunCreate(context.Background(), opID); err == nil {
		t.Fatal("expected lost CREATE_REPOSITORY response")
	}
	pending, err := store.Get(opID)
	if err != nil || pending.State != StateProvisioningRequested || len(transport.tickets) != 2 {
		t.Fatalf("pending=%+v tickets=%d err=%v", pending, len(transport.tickets), err)
	}
	completed, err := orchestrator.RunCreate(context.Background(), opID)
	if err != nil || completed.State != StateActive || len(transport.tickets) != 4 {
		t.Fatalf("completed=%+v tickets=%d err=%v", completed, len(transport.tickets), err)
	}
	if transport.tickets[1].RequestID != transport.tickets[2].RequestID {
		t.Fatalf("CREATE request ID changed across retry: %s != %s", transport.tickets[1].RequestID, transport.tickets[2].RequestID)
	}
}

type fakeControlExchange struct {
	types           []control.TicketType
	tickets         []control.Ticket
	failCreateOnce  bool
	failInitialOnce bool
}

func (f *fakeControlExchange) Exchange(_ context.Context, ticket control.Ticket) (control.Result, error) {
	f.types = append(f.types, ticket.Type)
	f.tickets = append(f.tickets, ticket)
	if ticket.Type == control.TicketCreateRepository && f.failCreateOnce {
		f.failCreateOnce = false
		return control.Result{}, errors.New("connection lost after CREATE_REPOSITORY")
	}
	if ticket.Type == control.TicketInitialCommit && f.failInitialOnce {
		f.failInitialOnce = false
		return control.Result{}, errors.New("connection lost after local publication")
	}
	var payload any
	switch ticket.Type {
	case control.TicketStoragePreflight:
		payload = control.StoragePreflightResult{AvailableBytes: 1 << 30, RequiredBytes: 64 << 20}
	case control.TicketCreateRepository:
		payload = control.CreateRepositoryResult{RepoID: "repo", RepoURL: "file:///repo"}
	case control.TicketInitialCommit:
		payload = control.InitialCommitResult{Acknowledged: true}
	default:
		return control.Result{}, errors.New("unexpected ticket")
	}
	raw, _ := json.Marshal(payload)
	return control.Result{Schema: control.Schema, OperationID: ticket.OperationID, RequestID: ticket.RequestID, Type: ticket.Type, Status: control.ResultOK, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: raw}, nil
}
