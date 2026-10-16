package repoworker

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"filees/public-shares/channel"
	"github.com/google/uuid"
)

func TestPublicShareCanonicalReceiptsAreOptInAndMatchState(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "receipt"}[include], func(t *testing.T) {
			owner, repo := uuid.NewString(), uuid.NewString()
			channels := &channel.Store{Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "pracownia"}, TokenKey: []byte(strings.Repeat("t", 32))}
			service := ChannelPublicShareService{Channels: channels, BaseURL: "https://files.example", Deliverer: &shareDeliverer{}}
			store, _ := NewFileStore(t.TempDir())
			worker := Worker{Store: store, PublicShares: service}
			session := Session{ClientID: uuid.NewString(), RealmID: owner, CanCreateRepositories: true, Repositories: []clientview.Repository{{RepoID: repo, DisplayName: "Project"}}}
			exchange := func(typ control.TicketType, payload any) control.PublicShareResult {
				t.Helper()
				ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, session.ClientID, payload, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				wire, err := worker.Handle(t.Context(), session, ticket)
				if err != nil || wire.Status != control.ResultOK {
					t.Fatalf("wire=%+v err=%v", wire, err)
				}
				var result control.PublicShareResult
				if err := control.DecodeResultPayload(wire.Result, &result); err != nil {
					t.Fatal(err)
				}
				if (result.StateReceipt != nil) != include {
					t.Fatal("receipt opt-in violated")
				}
				if bytes.Contains(wire.Result, []byte("password_hash")) || bytes.Contains(wire.Result, []byte("invitation_message")) || bytes.Contains(wire.Result, []byte("token")) {
					t.Fatal("receipt leaked credentials or invitation text")
				}
				if include {
					r := result.StateReceipt
					if r.RealmID != owner || r.RepoID != repo || r.RepoDisplayName != "Project" {
						t.Fatal("receipt binding lost")
					}
					if result.State != "deleted" {
						rows, err := service.ListOwnedRepositories(t.Context(), owner, []string{repo})
						if err != nil || len(rows) != 1 {
							t.Fatalf("canonical list: %v", err)
						}
						rows[0].InvitationMessage = ""
						if !reflect.DeepEqual(*r.Share, rows[0]) {
							t.Fatal("ACK differs from canonical state")
						}
					} else if r.Share != nil {
						t.Fatal("delete not a tombstone")
					}
				}
				return result
			}
			decl := shareDeclaration(repo)
			decl.IncludeState = include
			decl.ManageInvitations = true
			decl.InvitationMessage = "private invitation"
			created := exchange(control.TicketCreatePublicShare, control.CreatePublicSharePayload{PublicShareDeclaration: decl})
			decl.Objects[0].DisplayName = "Updated.pdf"
			exchange(control.TicketUpdatePublicShare, control.UpdatePublicSharePayload{ChannelID: created.ChannelID, PublicShareDeclaration: decl})
			exchange(control.TicketRevokePublicShare, control.RevokePublicSharePayload{ChannelID: created.ChannelID, IncludeState: include})
			exchange(control.TicketDeletePublicShare, control.DeletePublicSharePayload{ChannelID: created.ChannelID, IncludeState: include})
		})
	}
}
