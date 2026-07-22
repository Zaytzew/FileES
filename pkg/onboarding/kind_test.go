package onboarding

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreateTicketRejectsInvalidKind(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43000, 43010)
	defer store.Close()
	if _, err := store.CreateTicket("a@example.net", Policy{RealmID: testRealmID, Kind: "tablet"}, time.Hour); err == nil {
		t.Fatal("invalid kind accepted")
	}
}

func TestPolicyKindRoundTripsToActivationGrant(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43100, 43110)
	defer store.Close()

	if _, err := store.CreateTicket("mobile@example.net", Policy{RealmID: testRealmID, Kind: KindMobile}, time.Hour); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Take("mobile@example.net", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.GetOperation(receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.ApprovedPolicy.Kind != KindMobile {
		t.Fatalf("operation policy kind=%q, want %q", op.ApprovedPolicy.Kind, KindMobile)
	}

	entries, err := store.ListOutbox()
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%d err=%v", len(entries), err)
	}
	grant, err := store.AuthenticateOTP(entries[0].OTP)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ApprovedPolicy.Kind != KindMobile {
		t.Fatalf("auth grant policy kind=%q, want %q", grant.ApprovedPolicy.Kind, KindMobile)
	}

	activation, err := store.PendingActivation(OperationTunnelAuthorized)
	if err != nil {
		t.Fatal(err)
	}
	if activation.OperationID != receipt.OperationID {
		t.Fatalf("pending activation operation=%s, want %s", activation.OperationID, receipt.OperationID)
	}
	if activation.Kind != KindMobile {
		t.Fatalf("activation grant kind=%q, want %q", activation.Kind, KindMobile)
	}
}

func TestPolicyKindDefaultsToDesktopForPreExistingRecords(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43200, 43210)
	defer store.Close()

	// No Kind set at all - mirrors every ticket/operation persisted before
	// this field existed.
	if _, err := store.CreateTicket("desktop@example.net", Policy{RealmID: testRealmID}, time.Hour); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Take("desktop@example.net", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.GetOperation(receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.ApprovedPolicy.Kind != "" {
		t.Fatalf("operation policy kind=%q, want empty (desktop default)", op.ApprovedPolicy.Kind)
	}

	entries, err := store.ListOutbox()
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%d err=%v", len(entries), err)
	}
	if _, err := store.AuthenticateOTP(entries[0].OTP); err != nil {
		t.Fatal(err)
	}
	activation, err := store.PendingActivation(OperationTunnelAuthorized)
	if err != nil {
		t.Fatal(err)
	}
	if activation.Kind != "" {
		t.Fatalf("activation grant kind=%q, want empty (desktop default)", activation.Kind)
	}
}
