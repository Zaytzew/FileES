package contracttests

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

func TestShelfInvitationContracts(t *testing.T) {
	body := contract.UploadChannelDeclaration{AuthorityRepoID: uuid.NewString(), Slug: "test-shelf", Recipients: []string{"a@example.test"}, ManageInvitations: true, InvitationMessage: "Proszę o pliki.\nDziękuję!", ResendInvitations: true}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var remote control.UploadChannelDeclaration
	if err := control.DecodePayload(raw, &remote); err != nil {
		t.Fatal(err)
	}
	if !remote.ResendInvitations || !remote.ManageInvitations || remote.InvitationMessage != body.InvitationMessage {
		t.Fatal("invitation fields lost")
	}
	if _, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketUpdateUploadChannel, "client", control.UpdateUploadChannelPayload{ChannelID: uuid.NewString(), UploadChannelDeclaration: remote}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketCreateUploadChannel, "client", control.CreateUploadChannelPayload{UploadChannelDeclaration: remote}, time.Now()); err == nil {
		t.Fatal("create accepted resend")
	}
	for _, text := range []string{strings.Repeat("ą", 2001), "a\x00b", "a\x1bb", string([]byte{0xff})} {
		if err := manifest.ValidateInvitationMessage(text); err == nil {
			t.Fatal("invalid message accepted")
		}
	}
	if err := manifest.ValidateInvitationMessage(strings.Repeat("ą", 2000)); err != nil {
		t.Fatal(err)
	}
}
