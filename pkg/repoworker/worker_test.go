package repoworker

import (
	"context"
	"errors"
	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"github.com/google/uuid"
	"reflect"
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

type fakeCapacity struct {
	available, required int64
	calls               int
}

type fakeMobilePairingMinter struct {
	calls int
	realm string
	repos []MobilePairingRepoGrant
	err   error
}

func (m *fakeMobilePairingMinter) CreatePairing(realmID string, repos []MobilePairingRepoGrant) (string, time.Time, error) {
	m.calls++
	m.realm = realmID
	m.repos = repos
	if m.err != nil {
		return "", time.Time{}, m.err
	}
	return "TESTLOCA-TORXXXXXXXXXXXXXXXX", time.Now().Add(5 * time.Minute), nil
}

func mobilePairingTicket(t *testing.T, client string) control.Ticket {
	t.Helper()
	v, e := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketMobilePairing, client, control.MobilePairingPayload{}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func (c *fakeCapacity) Check(context.Context, int64) (int64, int64, error) {
	c.calls++
	return c.available, c.required, nil
}

func TestFormatBytesRendersHumanReadableMagnitudes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{187424908, "178.7 MiB"},
		{118276096, "112.8 MiB"},
		{1 << 30, "1.0 GiB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestWorkerStoragePreflightHardRefusalAndDurableSuccess(t *testing.T) {
	session := Session{ClientID: "client", RealmID: uuid.NewString(), CanCreateRepositories: true}
	newTicket := func() control.Ticket {
		ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketStoragePreflight, session.ClientID, control.StoragePreflightPayload{ContentBytes: 100, Paths: 2}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	store, _ := NewFileStore(t.TempDir())
	insufficient := &fakeCapacity{available: 99, required: 100}
	result, err := (&Worker{Capacity: insufficient, Store: store}).Handle(context.Background(), session, newTicket())
	if err != nil || result.Status != control.ResultError || result.Error == nil || result.Error.Code != "STORAGE_INSUFFICIENT" {
		t.Fatalf("refusal=%+v err=%v", result, err)
	}
	if result.Error.Details["available_bytes"] != "99" || result.Error.Details["required_bytes"] != "100" {
		t.Fatalf("refusal details=%v", result.Error.Details)
	}
	if result.Error.Message != "server storage requires 100 B, 99 B available" {
		t.Fatalf("refusal message should be human-readable, got %q", result.Error.Message)
	}

	capacity := &fakeCapacity{available: 1000, required: 300}
	ticket := newTicket()
	worker := &Worker{Capacity: capacity, Store: store}
	first, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || first.Status != control.ResultOK {
		t.Fatalf("success=%+v err=%v", first, err)
	}
	second, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || second.CompletedAt != first.CompletedAt || capacity.calls != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", second, capacity.calls, err)
	}
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

func TestWorkerCarriesReservationThroughCreationAndReleasesAfterActivation(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	opID := uuid.NewString()
	store, _ := NewFileStore(t.TempDir())
	capacity := &fakeCapacity{available: 1000, required: 300}
	reservations := &FileReservationLedger{Root: t.TempDir(), Capacity: capacity, TTL: time.Hour}
	worker := &Worker{Backend: &fakeBackend{}, Activator: &fakeActivator{}, Capacity: capacity, Reservations: reservations, Store: store, Now: func() time.Time { return now }}
	session := Session{ClientID: "client", RealmID: uuid.NewString(), CanCreateRepositories: true}

	preflight, _ := control.NewTicket(opID, uuid.NewString(), control.TicketStoragePreflight, session.ClientID, control.StoragePreflightPayload{ContentBytes: 100}, now)
	if result, err := worker.Handle(context.Background(), session, preflight); err != nil || result.Status != control.ResultOK {
		t.Fatalf("preflight=%+v err=%v", result, err)
	}
	create, _ := control.NewTicket(opID, uuid.NewString(), control.TicketCreateRepository, session.ClientID, control.CreateRepositoryPayload{Name: "Docs"}, now)
	if result, err := worker.Handle(context.Background(), session, create); err != nil || result.Status != control.ResultOK {
		t.Fatalf("create=%+v err=%v", result, err)
	}
	initial, _ := control.NewTicket(opID, uuid.NewString(), control.TicketInitialCommit, session.ClientID, control.InitialCommitPayload{RepoID: "repo-1", Revision: 0}, now)
	if result, err := worker.Handle(context.Background(), session, initial); err != nil || result.Status != control.ResultOK {
		t.Fatalf("initial=%+v err=%v", result, err)
	}
	if err := reservations.Ensure(context.Background(), opID, now); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("reservation remained after activation: %v", err)
	}
}

func TestWorkerMobilePairingUsesSessionRealmNotPayloadAndIsIdempotent(t *testing.T) {
	minter := &fakeMobilePairingMinter{}
	store, _ := NewFileStore(t.TempDir())
	w := &Worker{Store: store, MobilePairing: minter}
	realm := uuid.NewString()
	tk := mobilePairingTicket(t, "client-a")
	repoID := uuid.NewString()
	// No CanCreateRepositories needed - any authenticated session may pair a
	// mobile device into its own realm.
	session := Session{ClientID: "client-a", RealmID: realm, Repositories: []clientview.Repository{
		{RepoID: repoID, DisplayName: "Docs", URL: "svn+ssh://_filees-client@example.net/" + repoID, Access: "rw", State: "active", AttachmentPolicy: "optional"},
	}}

	result, err := w.Handle(context.Background(), session, tk)
	if err != nil || result.Status != control.ResultOK || minter.calls != 1 || minter.realm != realm {
		t.Fatalf("result=%+v minter calls=%d realm=%s err=%v", result, minter.calls, minter.realm, err)
	}
	wantGrants := []MobilePairingRepoGrant{{RepoID: repoID, Access: "rw", AttachmentPolicy: "optional"}}
	if !reflect.DeepEqual(minter.repos, wantGrants) {
		t.Fatalf("minter.repos=%+v, want %+v", minter.repos, wantGrants)
	}
	var payload control.MobilePairingResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil || payload.Token == "" {
		t.Fatalf("decode result=%+v err=%v", payload, err)
	}

	// A fresh worker instance must reuse the durable result, not mint again.
	replay, err := (&Worker{Store: store, MobilePairing: minter}).Handle(context.Background(), session, tk)
	if err != nil || minter.calls != 1 || replay.CompletedAt != result.CompletedAt {
		t.Fatalf("replay=%+v minter calls=%d err=%v", replay, minter.calls, err)
	}
}

func TestWorkerMobilePairingFailsClosedWithoutMinterOrOnMinterError(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	session := Session{ClientID: "client-a", RealmID: uuid.NewString()}

	unconfigured := &Worker{Store: store}
	tk := mobilePairingTicket(t, "client-a")
	if result, err := unconfigured.Handle(context.Background(), session, tk); err != nil || result.Status != control.ResultError {
		t.Fatalf("unconfigured worker result=%+v err=%v", result, err)
	}

	failing := &Worker{Store: store, MobilePairing: &fakeMobilePairingMinter{err: errors.New("disk full")}}
	tk2 := mobilePairingTicket(t, "client-a")
	if result, err := failing.Handle(context.Background(), session, tk2); err != nil || result.Status != control.ResultError {
		t.Fatalf("failing minter result=%+v err=%v", result, err)
	}
}
