package onboarding

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// testBarePublicKey generates a real (but throwaway) bare Ed25519
// authorized_keys line - ClaimAuthorizedMobilePush requires the pushed key
// to be bare (no comment), since the client cannot know its own client_id
// (needed for the "filees:<clientID>" comment activation.validateGrant
// requires) until after the server assigns one; the server appends that
// comment itself once it knows which operation this is.
func testBarePublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

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
// KindMobile. It also returns the OTP itself: ClaimAuthorizedMobilePush now
// requires the pairing token as its correlation key (the locator half
// re-derives the exact operation instead of scanning for "the only one" -
// see files.go's ClaimAuthorizedMobilePush doc comment).
func createMobileAuthorizedOperation(t *testing.T, store *Files, email string) (TakeReceipt, string) {
	t.Helper()
	return createAuthorizedOperation(t, store, email, KindMobile)
}

// createAuthorizedOperation is the Kind-parameterized form, used directly by
// tests that need a non-mobile operation concurrently authorized (e.g. to
// prove ClaimAuthorizedMobilePush's Kind filter).
func createAuthorizedOperation(t *testing.T, store *Files, email string, kind string) (TakeReceipt, string) {
	t.Helper()
	if _, err := store.CreateTicket(email, Policy{RealmID: testRealmID, Kind: kind}, time.Hour); err != nil {
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
			return receipt, entry.OTP
		}
	}
	t.Fatalf("no outbox entry for operation %s", receipt.OperationID)
	return TakeReceipt{}, ""
}

func TestClaimAuthorizedMobilePushClaimsTheAuthorizedOperation(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43300, 43310)
	defer store.Close()

	receipt, token := createMobileAuthorizedOperation(t, store, "push@example.net")
	bareKey := testBarePublicKey(t)
	const fingerprint = "SHA256:test-fingerprint"

	grant, err := store.ClaimAuthorizedMobilePush(token, bareKey, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if grant.OperationID != receipt.OperationID || grant.Kind != KindMobile {
		t.Fatalf("grant=%+v, want operation %s kind %s", grant, receipt.OperationID, KindMobile)
	}
	wantKey := bareKey + " filees:" + grant.ClientID
	if grant.InstallationPublicKey != wantKey || grant.InstallationFingerprint != fingerprint {
		t.Fatalf("grant identity=%+v, want key=%q fingerprint=%q", grant, wantKey, fingerprint)
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
	if op.InstallationPublicKey != wantKey || op.InstallationFingerprint != fingerprint {
		t.Fatalf("persisted identity=%+v", op)
	}
}

func TestClaimAuthorizedMobilePushRejectsCommentedKey(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43350, 43360)
	defer store.Close()

	_, token := createMobileAuthorizedOperation(t, store, "push-commented@example.net")
	if _, err := store.ClaimAuthorizedMobilePush(token, testBarePublicKey(t)+" filees:already-commented", "SHA256:x"); err == nil {
		t.Fatal("a client-supplied comment was accepted")
	}
}

func TestClaimAuthorizedMobilePushRejectsWhenNothingAuthorized(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43400, 43410)
	defer store.Close()

	// A syntactically valid but unmatched token: no operation exists at
	// all, so parseOTP succeeds but the locator matches nothing.
	token, _, err := generateOTP(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimAuthorizedMobilePush(token, testBarePublicKey(t), "SHA256:x"); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("push with nothing authorized error=%v", err)
	}
}

func TestClaimAuthorizedMobilePushRejectsSecondClaim(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43500, 43510)
	defer store.Close()

	_, token := createMobileAuthorizedOperation(t, store, "push-once@example.net")
	if _, err := store.ClaimAuthorizedMobilePush(token, testBarePublicKey(t), "SHA256:x"); err != nil {
		t.Fatal(err)
	}
	// The same token cannot claim twice: its locator was blanked at first
	// consumption, so replaying it now matches no live operation.
	if _, err := store.ClaimAuthorizedMobilePush(token, testBarePublicKey(t), "SHA256:x"); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("second push error=%v", err)
	}
}

func TestClaimAuthorizedMobilePushRejectsEmptyIdentity(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43600, 43610)
	defer store.Close()

	_, token := createMobileAuthorizedOperation(t, store, "push-empty@example.net")
	if _, err := store.ClaimAuthorizedMobilePush(token, "", "SHA256:x"); err == nil {
		t.Fatalf("empty public key accepted")
	}
	if _, err := store.ClaimAuthorizedMobilePush(token, testBarePublicKey(t), ""); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("empty fingerprint error=%v", err)
	}
}

// TestClaimAuthorizedMobilePushRejectsWrongLocator is the direct regression
// test for the concurrent-pairing hijack/deadlock bug: two mobile operations
// live in OperationTunnelAuthorized at the same time, and each token must
// claim only its own operation, never the other's.
func TestClaimAuthorizedMobilePushRejectsWrongLocator(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43700, 43720)
	defer store.Close()

	receiptA, tokenA := createMobileAuthorizedOperation(t, store, "phone-a@example.net")
	receiptB, tokenB := createMobileAuthorizedOperation(t, store, "phone-b@example.net")

	opB, err := store.GetOperation(receiptB.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if opB.State != OperationTunnelAuthorized {
		t.Fatalf("operation B state=%q, want still %q before any claim", opB.State, OperationTunnelAuthorized)
	}

	grantA, err := store.ClaimAuthorizedMobilePush(tokenA, testBarePublicKey(t), "SHA256:a")
	if err != nil {
		t.Fatal(err)
	}
	if grantA.OperationID != receiptA.OperationID {
		t.Fatalf("token A claimed operation %s, want %s", grantA.OperationID, receiptA.OperationID)
	}

	opB, err = store.GetOperation(receiptB.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if opB.State != OperationTunnelAuthorized {
		t.Fatalf("operation B state=%q, want still %q after A's claim", opB.State, OperationTunnelAuthorized)
	}

	grantB, err := store.ClaimAuthorizedMobilePush(tokenB, testBarePublicKey(t), "SHA256:b")
	if err != nil {
		t.Fatal(err)
	}
	if grantB.OperationID != receiptB.OperationID {
		t.Fatalf("token B claimed operation %s, want %s", grantB.OperationID, receiptB.OperationID)
	}
}

// TestClaimAuthorizedMobilePushRejectsDesktopKindOperation covers the Kind
// filter independently of locator matching: a desktop-kind operation's own
// (correctly formatted, correctly locatored) token must still never be
// accepted by the mobile push path.
func TestClaimAuthorizedMobilePushRejectsDesktopKindOperation(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 43800, 43810)
	defer store.Close()

	_, desktopToken := createAuthorizedOperation(t, store, "desktop-concurrent@example.net", KindDesktop)
	if _, err := store.ClaimAuthorizedMobilePush(desktopToken, testBarePublicKey(t), "SHA256:x"); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("mobile push against desktop-kind operation error=%v, want ErrTunnelGrant", err)
	}
}
