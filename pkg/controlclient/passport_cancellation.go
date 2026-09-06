package controlclient

import (
	"context"
	"errors"
	"fmt"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
)

// CancelPassportPreparation returns nil only for a matched cancellation receipt.
// No retry of prepare and no inferred success from an absent/expired SVN lock.
func CancelPassportPreparation(ctx context.Context, transport Exchanger, original control.Ticket) error {
	ticket, err := control.NewPassportCancellation(original)
	if err != nil {
		return err
	}
	if transport == nil {
		return errcat.New(errcat.KeyPassportUnavailable, nil, nil)
	}
	r, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.OperationID != ticket.OperationID || r.RequestID != ticket.RequestID || r.Type != ticket.Type {
		return errors.New("cancellation response mismatch")
	}
	if r.Status != control.ResultOK {
		if spec, ok := errcat.ByPair(errcat.Code(r.Error.Code), errcat.Key(r.Error.Message)); ok {
			return errcat.Of(spec.Code, spec.Key, nil, nil)
		}
		return fmt.Errorf("passport cancellation failed: %s: %s", r.Error.Code, r.Error.Message)
	}
	var proof control.CancelPassportPreparationResult
	if err := control.DecodePayload(r.Result, &proof); err != nil {
		return err
	}
	if proof.PreparationOperationID != original.OperationID || proof.PreparationRequestID != original.RequestID {
		return errors.New("cancellation receipt does not match preparation")
	}
	return nil
}
