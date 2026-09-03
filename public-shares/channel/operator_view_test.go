package channel

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The operator of a server must be able to ask what it publishes. Until this
// existed there was no way at all: shares are created from the client and the
// only record of what is live was the client's own projection. Measured on
// 2026-09-03, an alias returning 404 could not be distinguished from an alias
// that was never published - by an agent with a shell on the box, and equally
// by the owner, who could not remember the slug either.
func TestListAllAnswersWithoutARealm(t *testing.T) {
	store, share, owner := fixture(t)
	live := uuid.NewString()
	if _, _, err := store.Create(live, owner, share); err != nil {
		t.Fatal(err)
	}
	second := share
	second.Slug = "drugie"
	withdrawn := uuid.NewString()
	if _, _, err := store.Create(withdrawn, owner, second); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(owner, withdrawn); err != nil {
		t.Fatal(err)
	}

	records, err := store.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ChannelID != live {
		t.Fatalf("a deleted channel is not a share any more: %+v", records)
	}
}

// Withdrawal on the authority of holding the machine, not of owning the realm.
// An abusive or mistaken link has to come down from the server that serves it,
// and the operator has no realm to present.
func TestOperatorWithdrawsWithoutOwningTheRealm(t *testing.T) {
	store, share, owner := fixture(t)
	channelID := uuid.NewString()
	if _, _, err := store.Create(channelID, owner, share); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(uuid.NewString(), channelID); err == nil {
		t.Fatal("a stranger's realm must still be refused")
	}
	record, err := store.DeleteAsOperator(channelID)
	if err != nil {
		t.Fatalf("the operator holding the disk must be able to withdraw: %v", err)
	}
	if record.State != StateDeleted || record.Manifest != nil || record.Recipients != nil {
		t.Fatalf("withdrawal must clear what it withdraws: %+v", record)
	}
	// The address stays reserved. Freeing it would let a later share inherit
	// an old link, which is the one outcome a withdrawal must never produce.
	if _, _, err := store.Create(uuid.NewString(), owner, share); err == nil {
		t.Fatal("a withdrawn address must not be rebindable")
	}
}

// The operator is owed the gate, never the secret. An operator holding the
// disk can read the record file, so this is not a boundary - it keeps the
// credential out of scrollback, shell history and pasted bug reports.
func TestOperatorListingCarriesNoCredential(t *testing.T) {
	store, share, owner := fixture(t)
	// Recipients cleared first: a shared password on a closed channel is
	// refused by construction, because it spreads through informal chains and
	// cannot be observed or withdrawn.
	share.Recipients = nil
	share.Password = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, _, err := store.Create(uuid.NewString(), owner, share); err != nil {
		t.Fatal(err)
	}
	records, err := store.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	// ListAll returns the canonical record, so the summary layer is what must
	// drop the credential; this asserts the credential is there to be dropped,
	// which is what makes the servertool test meaningful rather than vacuous.
	if records[0].Manifest == nil || !strings.HasPrefix(records[0].Manifest.Password, "$argon2id$") {
		t.Fatalf("fixture no longer carries a password, so the summary test proves nothing: %+v", records[0].Manifest)
	}
}
