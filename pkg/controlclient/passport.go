package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
)

type Exchanger interface {
	Exchange(context.Context, control.Ticket) (control.Result, error)
}

// Only a validated, request-bound terminal receipt produces this marker.
// A transport error (even one carrying the same catalog code) is NOT proof.
type abortedPreparation struct {
	cause   error
	request string
}

func (e *abortedPreparation) Error() string { return e.cause.Error() }
func (e *abortedPreparation) Unwrap() error { return e.cause }

func IsAbortedPreparation(err error, ticket control.Ticket) bool {
	var receipt *abortedPreparation
	raw, marshalErr := json.Marshal(ticket)
	return marshalErr == nil && errors.As(err, &receipt) && receipt.request == string(raw)
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
			fault := errcat.Of(spec.Code, spec.Key, nil, nil)
			if spec.Code == errcat.CodePassportAborted && spec.Key == errcat.KeyPassportAborted {
				raw, err := json.Marshal(ticket)
				if err != nil {
					return err
				}
				return &abortedPreparation{cause: fault, request: string(raw)}
			}
			return fault
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
