package onboarding

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCreateMobilePairingRejectsInvalidRealmID(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43700, 43710)
	defer store.Close()
	if _, _, err := store.CreateMobilePairing("not-a-uuid"); err == nil {
		t.Fatal("invalid realm_id accepted")
	}
}

func TestCreateMobilePairingTokenRoundTripsToPushableOperation(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43800, 43810)
	defer store.Close()

	token, receipt, err := store.CreateMobilePairing(testRealmID)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || receipt.OperationID == "" {
		t.Fatalf("token/receipt not populated: token=%q receipt=%+v", token, receipt)
	}

	op, err := store.GetOperation(receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != OperationAwaitingTunnel {
		t.Fatalf("operation state=%q, want %q", op.State, OperationAwaitingTunnel)
	}
	if op.ApprovedPolicy.RealmID != testRealmID || op.ApprovedPolicy.Kind != KindMobile {
		t.Fatalf("operation policy=%+v, want realm=%s kind=%s", op.ApprovedPolicy, testRealmID, KindMobile)
	}
	// No ticket, no e-mail, no port - a mobile pairing has none of these.
	if op.AssignedReversePort != 0 {
		t.Fatalf("operation allocated a reverse port=%d, want 0 (mobile never uses one)", op.AssignedReversePort)
	}
	if tickets, err := store.ListTickets(); err != nil || len(tickets) != 0 {
		t.Fatalf("mobile pairing created a ticket: tickets=%v err=%v", tickets, err)
	}
	if entries, err := store.ListOutbox(); err != nil || len(entries) != 0 {
		t.Fatalf("mobile pairing created an outbox entry: entries=%v err=%v", entries, err)
	}

	// The token is presented exactly where a desktop client would present
	// its mailed OTP - same AuthenticateOTP, same downstream
	// ClaimAuthorizedMobilePush, both already covered elsewhere; here we
	// only need to confirm the token this method returns actually works.
	grant, err := store.AuthenticateOTP(token)
	if err != nil {
		t.Fatal(err)
	}
	if grant.OperationID != receipt.OperationID {
		t.Fatalf("auth grant operation=%s, want %s", grant.OperationID, receipt.OperationID)
	}

	pending, err := store.PendingActivation(OperationTunnelAuthorized)
	if err != nil {
		t.Fatal(err)
	}
	if pending.OperationID != receipt.OperationID || pending.Kind != KindMobile {
		t.Fatalf("pending activation=%+v, want operation=%s kind=%s", pending, receipt.OperationID, KindMobile)
	}

	pushed, err := store.ClaimAuthorizedMobilePush(testBarePublicKey(t), "SHA256:test")
	if err != nil {
		t.Fatal(err)
	}
	if pushed.OperationID != receipt.OperationID {
		t.Fatalf("pushed grant operation=%s, want %s", pushed.OperationID, receipt.OperationID)
	}
}

func TestCreateMobilePairingUsesAShortDefaultTTL(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43900, 43910)
	defer store.Close()

	_, receipt, err := store.CreateMobilePairing(testRealmID)
	if err != nil {
		t.Fatal(err)
	}
	ttl := receipt.ExpiresAt.Sub(now)
	if ttl <= 0 || ttl > time.Hour {
		t.Fatalf("mobile pairing TTL=%s, want a short (default 5m) window, not an admin-ticket-length one", ttl)
	}
}

func TestCreateMobilePairingRequiresOTPSecretAndOperationsArea(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "onboarding")
	if err := Initialize(root); err != nil {
		t.Fatal(err)
	}
	// Areas declared without AreaOperations|AreaAudit and without OTP access
	// - mirrors how a narrowly-scoped tool invocation would be denied.
	store, err := OpenExisting(root, testOptions(&now, 3, 44000, 44010), Access{Areas: AreaTickets, NeedOTP: false})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.CreateMobilePairing(testRealmID); err == nil {
		t.Fatal("narrowly-scoped access (no AreaOperations, no OTP) unexpectedly allowed CreateMobilePairing")
	}
}
