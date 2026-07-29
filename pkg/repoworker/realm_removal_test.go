package repoworker

import (
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
