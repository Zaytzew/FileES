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
	return Repository{RepoID: "33333333-3333-4333-8333-333333333333", URL: "svn://example/repo"}, nil
}
func (b *retryBackend) Delete(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, nil
}

type fakeBackend struct {
	calls       int
	deleteCalls int
	realm       string
	deletedRepo string
}

type fakeActivator struct {
	calls int
	repo  string
	realm string
}

type fakeCapacity struct {
	available, required int64
	calls               int
	err                 error
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
	if c.err != nil {
		return 0, 0, c.err
	}
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
	return Repository{RepoID: "22222222-2222-4222-8222-222222222222", URL: "svn://example/repo-1"}, nil
}
func (b *fakeBackend) Delete(_ context.Context, _ string, realm, repoID string) (time.Time, error) {
	b.deleteCalls++
	b.realm = realm
	b.deletedRepo = repoID
	return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC), nil
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

func TestWorkerDeleteUsesAuthenticatedRealmAndIsIdempotent(t *testing.T) {
	backend := &fakeBackend{}
	store, _ := NewFileStore(t.TempDir())
	worker := &Worker{Backend: backend, Store: store}
	realm, repoID := uuid.NewString(), uuid.NewString()
	session := Session{ClientID: "client-a", RealmID: realm, CanCreateRepositories: true}
	ticket, err := control.NewTicket(
		uuid.NewString(), uuid.NewString(), control.TicketDeleteRepository,
		session.ClientID, control.DeleteRepositoryPayload{RepoID: repoID}, time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || first.Status != control.ResultOK {
		t.Fatalf("delete=%+v err=%v", first, err)
	}
	if backend.deleteCalls != 1 || backend.realm != realm || backend.deletedRepo != repoID {
		t.Fatalf("backend calls=%d realm=%s repo=%s", backend.deleteCalls, backend.realm, backend.deletedRepo)
	}
	var payload control.DeleteRepositoryResult
	if err := control.DecodeResultPayload(first.Result, &payload); err != nil || payload.RepoID != repoID || payload.RetainUntil == "" {
		t.Fatalf("result=%+v err=%v", payload, err)
	}
	replay, err := (&Worker{Backend: backend, Store: store}).Handle(context.Background(), session, ticket)
	if err != nil || replay.CompletedAt != first.CompletedAt || backend.deleteCalls != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, backend.deleteCalls, err)
	}
}

func TestWorkerDeleteRequiresRepositoryAdministrationCapability(t *testing.T) {
	backend := &fakeBackend{}
	store, _ := NewFileStore(t.TempDir())
	worker := &Worker{Backend: backend, Store: store}
	session := Session{ClientID: "managed", RealmID: uuid.NewString()}
	ticket, err := control.NewTicket(
		uuid.NewString(), uuid.NewString(), control.TicketDeleteRepository,
		session.ClientID, control.DeleteRepositoryPayload{RepoID: uuid.NewString()}, time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || result.Status != control.ResultError || result.Error == nil || result.Error.Code != "DELETE_REPOSITORY_FORBIDDEN" {
		t.Fatalf("forbidden result=%+v err=%v", result, err)
	}
	if backend.deleteCalls != 0 {
		t.Fatalf("forbidden request reached backend: %d", backend.deleteCalls)
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
	initial, err := control.NewTicket(opID, uuid.NewString(), control.TicketInitialCommit, session.ClientID, control.InitialCommitPayload{RepoID: "22222222-2222-4222-8222-222222222222", Revision: 0, Paths: 0}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, err := w.Handle(context.Background(), session, initial)
	if err != nil || first.Status != control.ResultOK || a.calls != 1 || a.repo != "22222222-2222-4222-8222-222222222222" || a.realm != realm {
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
	initial, _ := control.NewTicket(opID, uuid.NewString(), control.TicketInitialCommit, session.ClientID, control.InitialCommitPayload{RepoID: "22222222-2222-4222-8222-222222222222", Revision: 0}, now)
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

// TestStoragePreflightRefusalStaysRetryable is the regression test for the
// audit's Finding G. A transient shortage (disk momentarily full) must not bind
// the operation's terminal result: the sibling CREATE_REPOSITORY_RETRY and
// INITIAL_COMMIT_RETRY branches deliberately stay retryable for exactly this
// reason, and pkg/provisioning has no reset/abandon path, so persisting
// STORAGE_INSUFFICIENT stranded that (client, local path, name) tuple forever.
func TestStoragePreflightRefusalStaysRetryable(t *testing.T) {
	session := Session{ClientID: "client", RealmID: uuid.NewString(), CanCreateRepositories: true}
	store, _ := NewFileStore(t.TempDir())
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketStoragePreflight, session.ClientID, control.StoragePreflightPayload{ContentBytes: 100, Paths: 2}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	full := &fakeCapacity{available: 99, required: 100}
	worker := &Worker{Capacity: full, Store: store}
	refused, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || refused.Status != control.ResultError || refused.Error.Code != "STORAGE_INSUFFICIENT" {
		t.Fatalf("refusal=%+v err=%v", refused, err)
	}
	// Nothing may have been bound as this operation's terminal result.
	if stored, ok, err := store.Load(ticket.OperationID, ticket.Type); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatalf("STORAGE_INSUFFICIENT was persisted as terminal: %+v", stored)
	}

	// The operator frees space; the very same ticket must now succeed rather
	// than replay the cached refusal.
	freed := &fakeCapacity{available: 1000, required: 100}
	worker = &Worker{Capacity: freed, Store: store}
	retried, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || retried.Status != control.ResultOK {
		t.Fatalf("retry after freeing space=%+v err=%v", retried, err)
	}
	if freed.calls != 1 {
		t.Fatalf("retry did not re-check capacity: calls=%d", freed.calls)
	}
	// A success, by contrast, must still be durable.
	again, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || again.CompletedAt != retried.CompletedAt || freed.calls != 1 {
		t.Fatalf("success is not idempotent: %+v err=%v calls=%d", again, err, freed.calls)
	}
}

// TestStoragePreflightDependencyErrorStaysRetryable covers the same defect on
// the capacity-checker error path: a failing statfs is a temporarily
// unavailable dependency, not an invalid request.
func TestStoragePreflightDependencyErrorStaysRetryable(t *testing.T) {
	session := Session{ClientID: "client", RealmID: uuid.NewString(), CanCreateRepositories: true}
	store, _ := NewFileStore(t.TempDir())
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketStoragePreflight, session.ClientID, control.StoragePreflightPayload{ContentBytes: 100, Paths: 2}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	broken := &fakeCapacity{err: errors.New("statfs: input/output error")}
	result, err := (&Worker{Capacity: broken, Store: store}).Handle(context.Background(), session, ticket)
	if err != nil || result.Status != control.ResultError || result.Error.Code != "STORAGE_PREFLIGHT_FAILED" {
		t.Fatalf("dependency failure=%+v err=%v", result, err)
	}
	if _, ok, err := store.Load(ticket.OperationID, ticket.Type); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("STORAGE_PREFLIGHT_FAILED was persisted as terminal")
	}

	healthy := &fakeCapacity{available: 1000, required: 100}
	retried, err := (&Worker{Capacity: healthy, Store: store}).Handle(context.Background(), session, ticket)
	if err != nil || retried.Status != control.ResultOK {
		t.Fatalf("retry after recovery=%+v err=%v", retried, err)
	}
}
