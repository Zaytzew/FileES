package controlclient

import (
	"context"
	"errors"
	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
	"fmt"
)

// PassportExecution accepts only a complete, request-bound server receipt.
func PassportExecution(ctx context.Context, transport Exchanger, ticket control.Ticket) error {
	if transport == nil {
		return errcat.New(errcat.KeyPassportUnavailable, nil, nil)
	}
	if ticket.Type != control.TicketArmPassportAcquisition && ticket.Type != control.TicketSettlePassportAcquisition && ticket.Type != control.TicketExpirePassportPath {
		return errors.New("not an acquisition ticket")
	}
	if err := ticket.Validate(); err != nil {
		return err
	}
	var p control.PassportExecutionPayload
	if err := control.DecodePayload(ticket.Payload, &p); err != nil {
		return err
	}
	r, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return err
	}
	if err = r.Validate(); err != nil {
		return err
	}
	if r.OperationID != ticket.OperationID || r.RequestID != ticket.RequestID || r.Type != ticket.Type {
		return errors.New("acquisition response mismatch")
	}
	if r.Status != control.ResultOK {
		if spec, ok := errcat.ByPair(errcat.Code(r.Error.Code), errcat.Key(r.Error.Message)); ok {
			return errcat.Of(spec.Code, spec.Key, nil, nil)
		}
		return fmt.Errorf("passport acquisition failed: %s: %s", r.Error.Code, r.Error.Message)
	}
	var proof control.PassportExecutionResult
	if err = control.DecodePayload(r.Result, &proof); err != nil {
		return err
	}
	if proof.RepoID != p.RepoID || proof.Path != p.Path || proof.Comment != p.Comment {
		return errors.New("acquisition receipt mismatch")
	}
	return nil
}
