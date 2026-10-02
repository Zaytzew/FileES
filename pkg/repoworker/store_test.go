package repoworker

import (
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

func TestOperationBindingRejectsLegacyResultAndChangedRequest(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := Session{ClientID: "client-a", RealmID: uuid.NewString()}
	operationID, requestID := uuid.NewString(), uuid.NewString()
	ticket, err := control.NewTicket(operationID, requestID, control.TicketCreateRepository, session.ClientID, control.CreateRepositoryPayload{Name: "One"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(createRepositoryResult(t, operationID, requestID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Bind(ticket, session); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy result adopted: %v", err)
	}

	newTicket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketCreateRepository, session.ClientID, control.CreateRepositoryPayload{Name: "One"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Bind(newTicket, session); err != nil {
		t.Fatal(err)
	}
	changed := newTicket
	changed.Payload = []byte(`{"name":"Two"}`)
	if err := store.Bind(changed, session); err == nil {
		t.Fatal("changed payload reused operation binding")
	}
	changed = newTicket
	changed.RequestID = uuid.NewString()
	if err := store.Bind(changed, session); err == nil {
		t.Fatal("changed request reused operation binding")
	}
	numbered := newTicket
	numbered.OperationID = uuid.NewString()
	numbered.Payload = []byte(`{"count":9007199254740992}`)
	if err := store.Bind(numbered, session); err != nil {
		t.Fatal(err)
	}
	numbered.Payload = []byte(`{"count":9007199254740993}`)
	if err := store.Bind(numbered, session); err == nil {
		t.Fatal("distinct 64-bit JSON numbers collapsed into one binding")
	}
	if err := store.Bind(newTicket, session); err != nil {
		t.Fatalf("original retry refused: %v", err)
	}
}

func mobilePairingResult(t *testing.T, operationID, requestID string, expiresAt time.Time) control.Result {
	t.Helper()
	result, err := control.NewSuccessResult(operationID, requestID, control.TicketMobilePairing, control.MobilePairingResult{
		Token:     "LOCATOR1-SECRETXXXXXXXXXXXXXXXX",
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func createRepositoryResult(t *testing.T, operationID, requestID string) control.Result {
	t.Helper()
	result, err := control.NewSuccessResult(operationID, requestID, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: "repo", RepoURL: "svn://example/repo"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestPurgeExpiredMobilePairingsRemovesOnlyExpired is the A-05 regression
// guard: a stored MOBILE_PAIRING result is read back to make a retried
// control-plane request idempotent (Worker.Handle), so cleanup must only
// remove results whose token can never authenticate again - never a live
// one, and never a result from an unrelated ticket type.
func TestPurgeExpiredMobilePairingsRemovesOnlyExpired(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	expiredID, expiredRequest := uuid.NewString(), uuid.NewString()
	liveID, liveRequest := uuid.NewString(), uuid.NewString()
	otherID, otherRequest := uuid.NewString(), uuid.NewString()

	now := time.Now().UTC()
	if err := store.Save(mobilePairingResult(t, expiredID, expiredRequest, now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(mobilePairingResult(t, liveID, liveRequest, now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(createRepositoryResult(t, otherID, otherRequest)); err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeExpiredMobilePairings(now)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("purged=%d, want 1", purged)
	}

	if _, ok, err := store.Load(expiredID, control.TicketMobilePairing); err != nil || ok {
		t.Fatalf("expired result still present: ok=%v err=%v", ok, err)
	}
	if result, ok, err := store.Load(liveID, control.TicketMobilePairing); err != nil || !ok || result.RequestID != liveRequest {
		t.Fatalf("live result missing or wrong: ok=%v err=%v result=%+v", ok, err, result)
	}
	if result, ok, err := store.Load(otherID, control.TicketCreateRepository); err != nil || !ok || result.RequestID != otherRequest {
		t.Fatalf("non-mobile-pairing result touched: ok=%v err=%v result=%+v", ok, err, result)
	}

	// A second purge is safe and idempotent - nothing left to remove.
	if purged, err := store.PurgeExpiredMobilePairings(now); err != nil || purged != 0 {
		t.Fatalf("second purge=%d err=%v, want 0", purged, err)
	}
}
