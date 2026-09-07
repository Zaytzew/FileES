package contracttests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"github.com/google/uuid"
)

func TestPassportExecutionWireReceipts(t *testing.T) {
	for _, kind := range []control.TicketType{control.TicketArmPassportAcquisition, control.TicketSettlePassportAcquisition, control.TicketExpirePassportPath} {
		p := control.PassportExecutionPayload{RepoID: uuid.NewString(), Path: "Łódź/doc", Comment: "bound-comment"}
		state := "armed"
		if kind == control.TicketSettlePassportAcquisition {
			state = "closed"
		}
		if kind == control.TicketExpirePassportPath {
			state = "checked"
			p.Comment = ""
		}
		ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), kind, uuid.NewString(), p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(ticket)
		if _, err := control.ParseTicket(raw); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"", "outer", "path", "repo", "comment", "state"} {
			x := preparationExchange(func(_ context.Context, got control.Ticket) (control.Result, error) {
				proof := control.PassportExecutionResult{RepoID: p.RepoID, Path: p.Path, Comment: p.Comment, State: state}
				r, err := control.NewSuccessResult(got.OperationID, got.RequestID, got.Type, proof, time.Now())
				switch bad {
				case "outer":
					r.RequestID = uuid.NewString()
				case "path":
					proof.Path = "another"
				case "repo":
					proof.RepoID = uuid.NewString()
				case "comment":
					proof.Comment = "other"
				case "state":
					proof.State = "acquired"
				}
				r.Result, _ = json.Marshal(proof)
				return r, err
			})
			if err := controlclient.PassportExecution(t.Context(), x, ticket); (err == nil) != (bad == "") {
				t.Fatalf("%s %s: %v", kind, bad, err)
			}
		}
	}
}
