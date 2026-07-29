package repoworker

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDataErasureRequestRequiresConfirmedIntentAndExplicitOperatorCompletion(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := DataErasureStore{Root: t.TempDir(), Now: func() time.Time { return now }}
	operationID, realmID := uuid.NewString(), uuid.NewString()
	removal := RealmRemovalRecord{
		OperationID: operationID, RealmID: realmID,
		Request: RealmRemovalRequest{NotificationEmail: "User@Example.net", ErasureRequested: true},
	}
	if _, err := store.Accept(removal, 90); err == nil {
		t.Fatal("unconfirmed erasure request accepted")
	}
	removal.ConfirmedAt = &now
	record, err := store.Accept(removal, 90)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != DataErasureRequested || record.NotificationEmail != "User@example.net" ||
		!record.CompletionDueAt.Equal(now.AddDate(0, 0, 90)) {
		t.Fatalf("unexpected accepted request: %+v", record)
	}
	if _, err := store.Complete(operationID, false); err == nil {
		t.Fatal("request completed before active-data deletion")
	}
	record, err = store.MarkActiveDataDeleted(operationID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != DataErasureAwaitingBackupRetention || record.ActiveDataDeletedAt == nil {
		t.Fatalf("unexpected retention state: %+v", record)
	}
	record, err = store.Complete(operationID, true)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != DataErasurePartiallyRetained || record.CompletedAt == nil {
		t.Fatalf("unexpected completed request: %+v", record)
	}
	job, err := store.ClaimPendingMail(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.FinalState != DataErasurePartiallyRetained || job.DeliveryAddress != "User@example.net" {
		t.Fatalf("unexpected completion mail: %+v", job)
	}
	message, err := RenderDataErasureCompletionMail(job, "filees@example.net", "mail.example.net")
	if err != nil || !strings.Contains(string(message), "Some records remain retained") {
		t.Fatalf("unexpected completion message: %s err=%v", message, err)
	}
	if err := store.MarkMailQueued(operationID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.mailPath(operationID))
	if err != nil || strings.Contains(string(raw), "User@example.net") {
		t.Fatalf("queued completion outbox retained address: %s err=%v", raw, err)
	}
	record, err = store.Load(operationID)
	if err != nil || record.NotificationEmail != "" {
		t.Fatalf("completed erasure record retained notification address: %+v err=%v", record, err)
	}
}

func TestDataErasureAcceptIsIdempotentAndRejectsPolicyDrift(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := DataErasureStore{Root: t.TempDir(), Now: func() time.Time { return now }}
	removal := RealmRemovalRecord{
		OperationID: uuid.NewString(), RealmID: uuid.NewString(), ConfirmedAt: &now,
		Request: RealmRemovalRequest{NotificationEmail: "user@example.net", ErasureRequested: true},
	}
	first, err := store.Accept(removal, 90)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Accept(removal, 90)
	if err != nil || second != first {
		t.Fatalf("idempotent accept = %+v, %v; want %+v", second, err, first)
	}
	if _, err := store.Accept(removal, 91); err == nil {
		t.Fatal("policy drift accepted for existing request")
	}
}
