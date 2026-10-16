package controlclient

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestLargeCanonicalShareReceiptOverPinnedSSH(t *testing.T) {
	for _, kind := range []control.TicketType{control.TicketCreatePublicShare, control.TicketUpdatePublicShare, control.TicketRevokePublicShare} {
		t.Run(string(kind), func(t *testing.T) {
			repoID, channelID, realmID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			decl := control.PublicShareDeclaration{IncludeState: true, RepoID: repoID, SourceRoot: ".", Slug: "archive"}
			for i := 0; i < 4096; i++ {
				decl.Objects = append(decl.Objects, control.PublicShareObject{PublicID: fmt.Sprintf("%016x", i), RepoPath: fmt.Sprintf("folder/file-%04d.pdf", i), DisplayName: fmt.Sprintf("file-%04d.pdf", i)})
			}
			var payload any = control.CreatePublicSharePayload{PublicShareDeclaration: decl}
			if kind == control.TicketUpdatePublicShare {
				payload = control.UpdatePublicSharePayload{ChannelID: channelID, PublicShareDeclaration: decl}
			}
			if kind == control.TicketRevokePublicShare {
				payload = control.RevokePublicSharePayload{ChannelID: channelID, IncludeState: true}
			}
			ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), kind, uuid.NewString(), payload, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			client := sizeTestPeer(t, func(ch ssh.Channel, request control.Ticket) {
				state := "active"
				if kind == control.TicketRevokePublicShare {
					state = "revoked"
				}
				row := control.PublicShareSummary{ChannelID: channelID, RepoID: repoID, Alias: "lab", Slug: decl.Slug, State: state, SourceRoot: decl.SourceRoot, Objects: decl.Objects, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
				ack := control.PublicShareResult{ChannelID: channelID, Alias: "lab", Slug: decl.Slug, State: state, StateReceipt: &control.PublicShareStateReceipt{RealmID: realmID, RepoID: repoID, Share: &row}}
				result, err := control.NewSuccessResult(request.OperationID, request.RequestID, kind, ack, time.Now())
				if err != nil {
					return
				}
				_ = json.NewEncoder(ch).Encode(result)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			})
			result, err := client.Exchange(t.Context(), ticket)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Result) <= control.DefaultMessageBytes {
				t.Fatal("fixture too small")
			}
			var ack control.PublicShareResult
			if err := control.DecodeResultPayload(result.Result, &ack); err != nil {
				t.Fatal(err)
			}
			if ack.StateReceipt == nil || len(ack.StateReceipt.Share.Objects) != 4096 {
				t.Fatal("ACK truncated")
			}
		})
	}
}
