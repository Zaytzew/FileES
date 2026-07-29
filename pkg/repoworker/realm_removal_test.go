package repoworker

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRealmRemovalOTPConfirmsExactlyOneImmutableScope(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 3, Now: func() time.Time { return now }}
	realm, client, repo, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	record, otp, err := store.Begin(realm, RealmRemovalScope{ClientIDs: []string{client}, OwnedRepoIDs: []string{repo}, ForeignGrantRepoIDs: []string{foreign}}, RealmRemovalRequest{NotificationEmail: "User@Example.net", ErasureRequested: true})
	if err != nil || otp == "" || record.OTPHash == "" || record.State != RealmRemovalAwaitingConfirmation {
		t.Fatalf("begin=%+v otp=%q err=%v", record, otp, err)
	}
	if record.Request.NotificationEmail != "User@example.net" || !record.Request.ErasureRequested {
		t.Fatalf("request=%+v", record.Request)
	}
	confirmed, err := store.Confirm(record.OperationID, otp)
	if err != nil || confirmed.State != RealmRemovalDeleting || confirmed.OTPHash != "" || len(confirmed.Scope.OwnedRepoIDs) != 1 {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	if _, err := store.Confirm(record.OperationID, otp); err == nil {
		t.Fatal("OTP reused after confirmation")
	}
}

func TestRealmRemovalBeginOperationIsIdempotentWithoutSecondOTP(t *testing.T) {
	store := RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 3}
	operationID, realm := uuid.NewString(), uuid.NewString()
	first, otp, err := store.BeginOperation(operationID, realm, RealmRemovalScope{}, RealmRemovalRequest{NotificationEmail: "user@example.net"})
	if err != nil || otp == "" {
		t.Fatalf("first=%+v otp=%q err=%v", first, otp, err)
	}
	second, retryOTP, err := store.BeginOperation(operationID, realm, RealmRemovalScope{OwnedRepoIDs: []string{uuid.NewString()}}, RealmRemovalRequest{NotificationEmail: "user@example.net"})
	if err != nil || retryOTP != "" || second.OperationID != first.OperationID || len(second.Scope.OwnedRepoIDs) != 0 {
		t.Fatalf("retry=%+v otp=%q err=%v", second, retryOTP, err)
	}
	if _, _, err := store.BeginOperation(operationID, realm, RealmRemovalScope{}, RealmRemovalRequest{NotificationEmail: "other@example.net"}); err == nil {
		t.Fatal("conflicting operation request accepted")
	}
}

func TestRealmRemovalOTPExpiresWithoutDestructiveTransition(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Minute, Attempts: 1, Now: func() time.Time { return now }}
	record, otp, err := store.Begin(uuid.NewString(), RealmRemovalScope{}, RealmRemovalRequest{NotificationEmail: "otp@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := store.Confirm(record.OperationID, otp); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired OTP error=%v", err)
	}
}

func TestRealmRemovalAdvanceIsOrderedAndIdempotent(t *testing.T) {
	store := RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 2}
	record, otp, err := store.Begin(uuid.NewString(), RealmRemovalScope{}, RealmRemovalRequest{NotificationEmail: "state@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Confirm(record.OperationID, otp); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Advance(record.OperationID, RealmRemovalDeleting, RealmRemovalRevokingClients); err == nil {
		t.Fatal("skipped recovery boundary")
	}
	if next, err := store.Advance(record.OperationID, RealmRemovalDeleting, RealmRemovalRecoveryReady); err != nil || next.State != RealmRemovalRecoveryReady {
		t.Fatalf("recovery boundary=%+v err=%v", next, err)
	}
	if _, err = store.Advance(record.OperationID, RealmRemovalRecoveryReady, RealmRemovalRevokingClients); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Advance(record.OperationID, RealmRemovalRevokingClients, RealmRemovalCompleted); err != nil {
		t.Fatal(err)
	}
	if again, err := store.Advance(record.OperationID, RealmRemovalRevokingClients, RealmRemovalCompleted); err != nil || again.State != RealmRemovalCompleted {
		t.Fatalf("completed replay=%+v err=%v", again, err)
	}
}

func TestRealmRemovalMailOutboxScrubsOTPAfterQueueOrConfirmation(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 2, Now: func() time.Time { return now }}
	record, otp, err := store.Begin(uuid.NewString(), RealmRemovalScope{}, RealmRemovalRequest{NotificationEmail: "otp@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimPendingMail(time.Minute)
	if err != nil || job.OTP != otp || job.DeliveryState != RealmRemovalMailSending {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if err := store.MarkMailQueued(record.OperationID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.mailPath(record.OperationID))
	if err != nil || strings.Contains(string(raw), otp) || strings.Contains(string(raw), "otp@example.net") {
		t.Fatalf("queued outbox retained delivery secret: %s err=%v", raw, err)
	}

	record, otp, err = store.Begin(uuid.NewString(), RealmRemovalScope{}, RealmRemovalRequest{NotificationEmail: "confirm@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(record.OperationID, otp); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(store.mailPath(record.OperationID))
	if err != nil || strings.Contains(string(raw), otp) || strings.Contains(string(raw), "confirm@example.net") || !strings.Contains(string(raw), string(RealmRemovalMailCanceled)) {
		t.Fatalf("confirmed outbox retained delivery secret: %s err=%v", raw, err)
	}
}

func TestRealmRemovalMailOutboxDoesNotDeliverExpiredOTP(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Minute, Attempts: 2, Now: func() time.Time { return now }}
	record, otp, err := store.Begin(uuid.NewString(), RealmRemovalScope{}, RealmRemovalRequest{NotificationEmail: "expired@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := store.ClaimPendingMail(time.Minute); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired mail claim err=%v", err)
	}
	raw, err := os.ReadFile(store.mailPath(record.OperationID))
	if err != nil || strings.Contains(string(raw), otp) || strings.Contains(string(raw), "expired@example.net") || !strings.Contains(string(raw), string(RealmRemovalMailCanceled)) {
		t.Fatalf("expired outbox retained delivery secret: %s err=%v", raw, err)
	}
	expired, err := store.Load(record.OperationID)
	if err != nil || expired.State != RealmRemovalExpired {
		t.Fatalf("expired record=%+v err=%v", expired, err)
	}
}

func TestRenderRealmRemovalMailIncludesWarningAndOTP(t *testing.T) {
	created := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	job := RealmRemovalMailJob{Schema: realmRemovalMailSchema, MessageID: uuid.NewString(), OperationID: uuid.NewString(), DeliveryAddress: "user@example.net", OTP: "CONFIRMATIONCODE", DeliveryState: RealmRemovalMailSending, CreatedAt: created, ExpiresAt: created.Add(time.Hour)}
	message, err := RenderRealmRemovalMail(job, "filees@example.net", "mail.example.net")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FileES participation removal confirmation", "CONFIRMATIONCODE", "If this was not you", "all active client activations"} {
		if !strings.Contains(string(message), want) {
			t.Fatalf("mail lacks %q: %s", want, message)
		}
	}
}
