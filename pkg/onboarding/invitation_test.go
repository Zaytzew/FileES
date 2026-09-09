package onboarding

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestInvitationTicketRealmIsServerGeneratedNeverClientProposed is the
// regression test for the realm-join authorization gap
// (concepts/REALM_JOIN_AUTHORIZATION_CONCEPT.md): a plain, unbound
// invitation ticket must always end up with a *server-generated* realm,
// completely ignoring whatever the redeeming client proposed - including
// when that proposal happens to name an existing, unrelated realm the
// client has no authorization to join.
func TestInvitationTicketRealmIsServerGeneratedNeverClientProposed(t *testing.T) {
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42000, 42000)
	ticket, err := store.CreateInvitationTicket("alice@example.test", time.Hour, Invitation{
		ServerID: "office", ServerAddress: "filees.example.test:2222",
		KnownHost: "[filees.example.test]:2222 " + testReconnectPublicKey,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ticket.ApprovedPolicy.RealmID != "" || ticket.InvitationOutbox.Invitation == "" {
		t.Fatalf("ticket=%+v, want unbound ticket with invitation", ticket)
	}
	invite, err := DecodeInvitation(ticket.InvitationOutbox.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	// A malicious or buggy client proposing an existing, foreign realm - the
	// exact shape of the vulnerability this test guards against.
	foreignRealmID := uuid.NewString()
	receipt, err := store.TakeInvitation(invite.Token, foreignRealmID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.GetOperation(receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.ApprovedPolicy.RealmID != "" || op.ProposedRealmID != foreignRealmID {
		t.Fatalf("operation bound realm before OTP: %+v", op)
	}
	entries, err := store.ListOutbox()
	if err != nil || len(entries) != 1 || entries[0].OTP == "" {
		t.Fatalf("OTP outbox=%+v err=%v", entries, err)
	}
	grant, err := store.AuthenticateOTP(entries[0].OTP)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ApprovedPolicy.RealmID == "" {
		t.Fatalf("grant realm is empty, want a freshly generated realm")
	}
	if grant.ApprovedPolicy.RealmID == foreignRealmID {
		t.Fatalf("grant realm=%q equals the client's proposal - client-proposed realm must never be honored", grant.ApprovedPolicy.RealmID)
	}
	op, err = store.GetOperation(receipt.OperationID)
	if err != nil || op.ApprovedPolicy.RealmID != grant.ApprovedPolicy.RealmID || op.ProposedRealmID != "" {
		t.Fatalf("persisted realm op=%+v err=%v", op, err)
	}
}

// TestInvitationTicketWithAuthorizedRealmIgnoresClientProposal covers the
// join path: filees-admin ticket create --join-realm-alias (or the future
// self-service join-request confirm) pre-authorizes a realm on the ticket
// itself. The redeeming client's own proposal - whatever it is - must never
// override or need to match that pre-authorized realm.
func TestInvitationTicketWithAuthorizedRealmIgnoresClientProposal(t *testing.T) {
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42000, 42000)
	authorizedRealmID := uuid.NewString()
	ticket, err := store.CreateInvitationTicket("bob@example.test", time.Hour, Invitation{
		ServerID: "office", ServerAddress: "filees.example.test:2222",
		KnownHost: "[filees.example.test]:2222 " + testReconnectPublicKey,
	}, authorizedRealmID)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.ApprovedPolicy.RealmID != authorizedRealmID {
		t.Fatalf("ticket realm=%q, want %q", ticket.ApprovedPolicy.RealmID, authorizedRealmID)
	}
	invite, err := DecodeInvitation(ticket.InvitationOutbox.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	// The client, unaware of the authorized realm, proposes its own random
	// value as every deployed client does today - must be silently ignored.
	receipt, err := store.TakeInvitation(invite.Token, uuid.NewString(), uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox()
	if err != nil || len(entries) != 1 || entries[0].OTP == "" {
		t.Fatalf("OTP outbox=%+v err=%v", entries, err)
	}
	grant, err := store.AuthenticateOTP(entries[0].OTP)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ApprovedPolicy.RealmID != authorizedRealmID {
		t.Fatalf("grant realm=%q, want ticket-authorized %q", grant.ApprovedPolicy.RealmID, authorizedRealmID)
	}
	op, err := store.GetOperation(receipt.OperationID)
	if err != nil || op.ApprovedPolicy.RealmID != authorizedRealmID || op.ProposedRealmID != "" {
		t.Fatalf("persisted realm op=%+v err=%v", op, err)
	}
}

func TestInvitationTokenCannotBeReplacedByEmail(t *testing.T) {
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42000, 42000)
	ticket, err := store.CreateInvitationTicket("alice@example.test", time.Hour, Invitation{ServerID: "office", ServerAddress: "filees.example.test", KnownHost: "filees.example.test " + testReconnectPublicKey}, "")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := DecodeInvitation(ticket.InvitationOutbox.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TakeInvitation(strings.Repeat("A", len(invite.Token)), uuid.NewString(), uuid.NewString()); err != ErrTicketUnavailable {
		t.Fatalf("invalid capability error=%v", err)
	}
}
