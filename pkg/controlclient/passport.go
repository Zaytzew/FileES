package controlclient

import (
	"context"
	"errors"
	"fmt"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
)

type Exchanger interface {
	Exchange(context.Context, control.Ticket) (control.Result, error)
}

// PreparePassportReplacement reuses the pinned control-v1 transport. The
// caller must persist and retry the SAME ticket after transport uncertainty;
// this function never mints a fresh operation or acquires/forces an SVN lock.
func PreparePassportReplacement(ctx context.Context, transport Exchanger, ticket control.Ticket) error {
	if transport == nil {
		return errcat.New(errcat.KeyPassportUnavailable, nil, nil)
	}
	if err := ticket.Validate(); err != nil {
		return err
	}
	if ticket.Type != control.TicketPreparePassportReplacement {
		return errors.New("not a passport preparation ticket")
	}
	var p control.PreparePassportReplacementPayload
	if err := control.DecodePayload(ticket.Payload, &p); err != nil {
		return err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return err
	}
	if err := result.Validate(); err != nil {
		return err
	}
	if result.OperationID != ticket.OperationID || result.RequestID != ticket.RequestID || result.Type != ticket.Type {
		return errors.New("passport preparation response mismatch")
	}
	if result.Status != control.ResultOK {
		spec, ok := errcat.ByPair(errcat.Code(result.Error.Code), errcat.Key(result.Error.Message))
		if ok {
			return errcat.Of(spec.Code, spec.Key, nil, nil)
		}
		// Alpha keeps an unknown response visible instead of hiding catalog gaps.
		return fmt.Errorf("passport preparation failed: %s: %s", result.Error.Code, result.Error.Message)
	}
	var receipt control.PreparePassportReplacementResult
	if err := control.DecodePayload(result.Result, &receipt); err != nil {
		return err
	}
	if receipt.RepoID != p.RepoID || receipt.Path != p.Path || receipt.ObservedLockID != p.ObservedLockID {
		return errors.New("passport preparation receipt does not match requested token/path")
	}
	return nil
}
