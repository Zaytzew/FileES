package repoworker

import (
	"context"
	"errors"
	control "filees/pkg/control/v1"
	"github.com/google/uuid"
	"testing"
	"time"
)

type retryBackend struct{ calls int }

func (b *retryBackend) Create(context.Context, string, string, string) (Repository, error) {
	b.calls++
	if b.calls == 1 {
		return Repository{}, errors.New("temporary")
	}
	return Repository{RepoID: "repo", URL: "svn://example/repo"}, nil
}

type fakeBackend struct {
	calls int
	realm string
}

type fakeActivator struct {
	calls int
	repo  string
	realm string
}

func (a *fakeActivator) Activate(_ context.Context, repo, realm string) error {
	a.calls++
	a.repo, a.realm = repo, realm
	return nil
}

func TestWorkerRetriesSameOperationAfterBackendBoundaryFailure(t *testing.T) {
	b := &retryBackend{}
	store, _ := NewFileStore(t.TempDir())
	w := &Worker{Backend: b, Store: store}
	tk := ticket(t, "client")
	s := Session{ClientID: "client", RealmID: uuid.NewString(), CanCreateRepositories: true}
	first, e := w.Handle(context.Background(), s, tk)
	if e != nil || first.Status != control.ResultError {
		t.Fatalf("first=%+v err=%v", first, e)
	}
	second, e := w.Handle(context.Background(), s, tk)
	if e != nil || second.Status != control.ResultOK || b.calls != 2 {
		t.Fatalf("second=%+v calls=%d err=%v", second, b.calls, e)
	}
}

func (b *fakeBackend) Create(_ context.Context, _ string, realm, name string) (Repository, error) {
	b.calls++
	b.realm = realm
	return Repository{RepoID: "repo-1", URL: "svn://example/repo-1"}, nil
}
func ticket(t *testing.T, client string) control.Ticket {
	t.Helper()
	v, e := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketCreateRepository, client, control.CreateRepositoryPayload{Name: "Docs"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestWorkerUsesSessionRealmAndIsIdempotent(t *testing.T) {
	b := &fakeBackend{}
	store, _ := NewFileStore(t.TempDir())
	w := &Worker{Backend: b, Store: store}
	realm := uuid.NewString()
	tk := ticket(t, "client-a")
	s := Session{ClientID: "client-a", RealmID: realm, CanCreateRepositories: true}
	a, e := w.Handle(context.Background(), s, tk)
	if e != nil {
		t.Fatal(e)
	}
	// A fresh worker instance must reuse the durable result after restart.
	z, e := (&Worker{Backend: b, Store: store}).Handle(context.Background(), s, tk)
	if e != nil || b.calls != 1 || b.realm != realm || a.CompletedAt != z.CompletedAt {
		t.Fatalf("calls=%d realm=%s err=%v", b.calls, b.realm, e)
	}
}
func TestWorkerRejectsForgedClientAndCapability(t *testing.T) {
	b := &fakeBackend{}
	store, _ := NewFileStore(t.TempDir())
	w := &Worker{Backend: b, Store: store}
	tk := ticket(t, "payload-client")
	if _, e := w.Handle(context.Background(), Session{ClientID: "session-client", RealmID: uuid.NewString(), CanCreateRepositories: true}, tk); e == nil {
		t.Fatal("forged client accepted")
	}
	r, e := w.Handle(context.Background(), Session{ClientID: "payload-client", RealmID: uuid.NewString()}, tk)
	if e != nil || r.Status != control.ResultError || b.calls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", r, b.calls, e)
	}
}

func TestWorkerInitialCommitActivatesOwnerAndHasSeparateLedger(t *testing.T) {
	b := &fakeBackend{}
	a := &fakeActivator{}
	store, _ := NewFileStore(t.TempDir())
	w := &Worker{Backend: b, Activator: a, Store: store}
	realm := uuid.NewString()
	session := Session{ClientID: "client", RealmID: realm, CanCreateRepositories: true}
	opID := uuid.NewString()
	create, err := control.NewTicket(opID, uuid.NewString(), control.TicketCreateRepository, session.ClientID, control.CreateRepositoryPayload{Name: "Docs"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := w.Handle(context.Background(), session, create); err != nil || result.Status != control.ResultOK {
		t.Fatalf("create result=%+v err=%v", result, err)
	}
	initial, err := control.NewTicket(opID, uuid.NewString(), control.TicketInitialCommit, session.ClientID, control.InitialCommitPayload{RepoID: "repo-1", Revision: 0, Paths: 0}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, err := w.Handle(context.Background(), session, initial)
	if err != nil || first.Status != control.ResultOK || a.calls != 1 || a.repo != "repo-1" || a.realm != realm {
		t.Fatalf("initial=%+v activator=%+v err=%v", first, a, err)
	}
	second, err := (&Worker{Backend: b, Activator: a, Store: store}).Handle(context.Background(), session, initial)
	if err != nil || second.CompletedAt != first.CompletedAt || a.calls != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", second, a.calls, err)
	}
}
