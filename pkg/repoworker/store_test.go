package repoworker

import (
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

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
