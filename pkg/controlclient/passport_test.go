package controlclient

import (
	"context"
	"errors"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
	"github.com/google/uuid"
)

type passportExchange func(context.Context, control.Ticket) (control.Result, error)

func (f passportExchange) Exchange(ctx context.Context, ticket control.Ticket) (control.Result, error) {
	return f(ctx, ticket)
}

func TestAbortedPreparationRequiresBoundReceipt(t *testing.T) {
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketPreparePassportReplacement, uuid.NewString(), control.PreparePassportReplacementPayload{RepoID: uuid.NewString(), Path: "doc", ObservedLockID: "old-token", PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), Mode: "renew"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"receipt", "transport-fault", "other-request", "other-key", "unknown-code"} {
		t.Run(scenario, func(t *testing.T) {
			x := passportExchange(func(context.Context, control.Ticket) (control.Result, error) {
				if scenario == "transport-fault" {
					return control.Result{}, errcat.New(errcat.KeyPassportAborted, nil, nil)
				}
				body := control.ErrorBody{Code: string(errcat.CodePassportAborted), Message: string(errcat.KeyPassportAborted)}
				if scenario == "other-key" {
					body.Message = string(errcat.KeyPassportUncertain)
				}
				if scenario == "unknown-code" {
					body.Code = "LOCK-9999"
				}
				r, err := control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, body, time.Now())
				if scenario == "other-request" {
					r.RequestID = uuid.NewString()
				}
				return r, err
			})
			err := PreparePassportReplacement(t.Context(), x, ticket)
			if err == nil || IsAbortedPreparation(err, ticket) != (scenario == "receipt") {
				t.Fatalf("unexpected proof: %v", err)
			}
			if scenario == "receipt" {
				other := ticket
				other.RequestID = uuid.NewString()
				if IsAbortedPreparation(err, other) {
					t.Fatal("receipt reused for another intent")
				}
				var fault errcat.Fault
				if !errors.As(err, &fault) || fault.Key != errcat.KeyPassportAborted {
					t.Fatal("catalog identity lost")
				}
			}
		})
	}
}
