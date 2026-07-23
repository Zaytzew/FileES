package onboarding

import (
	"errors"
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

// createMobileAuthorizedOperation drives a ticket through Take and
// AuthenticateOTP so the resulting operation sits in OperationTunnelAuthorized
// - the state ClaimAuthorizedMobilePush claims from - with Policy.Kind ==
// KindMobile.
func createMobileAuthorizedOperation(t *testing.T, store *Files, email string) TakeReceipt {
	t.Helper()
	if _, err := store.CreateTicket(email, Policy{RealmID: testRealmID, Kind: KindMobile}, time.Hour); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Take(email, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.OperationID == receipt.OperationID {
			if _, err := store.AuthenticateOTP(entry.OTP); err != nil {
				t.Fatal(err)
			}
			return receipt
		}
	}
	t.Fatalf("no outbox entry for operation %s", receipt.OperationID)
	return TakeReceipt{}
}

func TestClaimAuthorizedMobilePushClaimsTheAuthorizedOperation(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43300, 43310)
	defer store.Close()

	receipt := createMobileAuthorizedOperation(t, store, "push@example.net")
	const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFZZUks/QxQg+QkoDHcY5mDZHBHpOd67MX6L3yjDH/UG filees:mobile-test"
	const fingerprint = "SHA256:test-fingerprint"

	grant, err := store.ClaimAuthorizedMobilePush(publicKey, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if grant.OperationID != receipt.OperationID || grant.Kind != KindMobile {
		t.Fatalf("grant=%+v, want operation %s kind %s", grant, receipt.OperationID, KindMobile)
	}
	if grant.InstallationPublicKey != publicKey || grant.InstallationFingerprint != fingerprint {
		t.Fatalf("grant identity=%+v, want key=%q fingerprint=%q", grant, publicKey, fingerprint)
	}
	// activation.validateGrant (pkg/activation) requires DeployRequestID to
	// be a UUID even though mobile has no reverse-tunnel deploy session to
	// correlate it with - Manager.Stage would otherwise reject every real
	// mobile grant.
	if _, err := uuid.Parse(grant.DeployRequestID); err != nil {
		t.Fatalf("grant DeployRequestID=%q is not a UUID: %v", grant.DeployRequestID, err)
	}

	op, err := store.GetOperation(receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != OperationIdentityGenerated {
		t.Fatalf("operation state=%q, want %q (tunnel_started/helper_announced skipped)", op.State, OperationIdentityGenerated)
	}
	if op.InstallationPublicKey != publicKey || op.InstallationFingerprint != fingerprint {
		t.Fatalf("persisted identity=%+v", op)
	}
}

func TestClaimAuthorizedMobilePushRejectsWhenNothingAuthorized(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43400, 43410)
	defer store.Close()

	if _, err := store.ClaimAuthorizedMobilePush("ssh-ed25519 AAAA filees:x", "SHA256:x"); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("push with nothing authorized error=%v", err)
	}
}

func TestClaimAuthorizedMobilePushRejectsSecondClaim(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43500, 43510)
	defer store.Close()

	createMobileAuthorizedOperation(t, store, "push-once@example.net")
	if _, err := store.ClaimAuthorizedMobilePush("ssh-ed25519 AAAA filees:x", "SHA256:x"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimAuthorizedMobilePush("ssh-ed25519 AAAA filees:x", "SHA256:x"); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("second push error=%v", err)
	}
}

func TestClaimAuthorizedMobilePushRejectsEmptyIdentity(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43600, 43610)
	defer store.Close()

	createMobileAuthorizedOperation(t, store, "push-empty@example.net")
	if _, err := store.ClaimAuthorizedMobilePush("", "SHA256:x"); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("empty public key error=%v", err)
	}
	if _, err := store.ClaimAuthorizedMobilePush("ssh-ed25519 AAAA filees:x", ""); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("empty fingerprint error=%v", err)
	}
}
