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
	record, otp, err := store.Begin(realm, RealmRemovalScope{ClientIDs: []string{client}, OwnedRepoIDs: []string{repo}, ForeignGrantRepoIDs: []string{foreign}})
	if err != nil || otp == "" || record.OTPHash == "" || record.State != RealmRemovalAwaitingConfirmation {
		t.Fatalf("begin=%+v otp=%q err=%v", record, otp, err)
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
	record, otp, err := store.Begin(uuid.NewString(), RealmRemovalScope{})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := store.Confirm(record.OperationID, otp); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired OTP error=%v", err)
	}
}
