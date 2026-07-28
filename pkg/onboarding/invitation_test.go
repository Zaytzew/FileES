package onboarding

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInvitationTicketBindsRealmOnlyAfterOTP(t *testing.T) {
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42000, 42000)
	ticket, err := store.CreateInvitationTicket("alice@example.test", time.Hour, Invitation{
		ServerID: "office", ServerAddress: "filees.example.test:2222",
		KnownHost: "[filees.example.test]:2222 " + testReconnectPublicKey,
	})
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
	realmID := uuid.NewString()
	receipt, err := store.TakeInvitation(invite.Token, realmID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.GetOperation(receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.ApprovedPolicy.RealmID != "" || op.ProposedRealmID != realmID {
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
	if grant.ApprovedPolicy.RealmID != realmID {
		t.Fatalf("grant realm=%q, want %q", grant.ApprovedPolicy.RealmID, realmID)
	}
	op, err = store.GetOperation(receipt.OperationID)
	if err != nil || op.ApprovedPolicy.RealmID != realmID || op.ProposedRealmID != "" {
		t.Fatalf("persisted realm op=%+v err=%v", op, err)
	}
}

func TestInvitationTokenCannotBeReplacedByEmail(t *testing.T) {
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42000, 42000)
	ticket, err := store.CreateInvitationTicket("alice@example.test", time.Hour, Invitation{ServerID: "office", ServerAddress: "filees.example.test", KnownHost: "filees.example.test " + testReconnectPublicKey})
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
