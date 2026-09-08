package controlclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	control "filees/pkg/control/v1"
	"golang.org/x/crypto/ssh"
)

// Only a transport interruption, never an authoritative rejection, host-pin
// error or malformed/mismatched receipt, permits an automatic replay.
func interruptedControlTransport(err error) bool {
	var missing *ssh.ExitMissingError
	var network net.Error
	return errors.As(err, &missing) || errors.As(err, &network) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// ResumeRepositoryDeletion exchanges exactly the caller's durable ticket.
// Unix workers serialize it under .worker.lock, and their result store and
// phase journal resume the same operation. No fresh deletion or recovery key
// is created when SSH loses the result. A worker/domain rejection is final
// for this call, even when its code contains RETRY (e.g. insufficient space).
func ResumeRepositoryDeletion(ctx context.Context, transport Exchanger, ticket control.Ticket) (control.Result, error) {
	return resumeRepositoryDeletion(ctx, transport, ticket, waitDeletionReconnect)
}

func resumeRepositoryDeletion(ctx context.Context, transport Exchanger, ticket control.Ticket, wait func(context.Context, time.Duration) error) (control.Result, error) {
	if transport == nil {
		return control.Result{}, errors.New("repository control transport is unavailable")
	}
	if err := ticket.Validate(); err != nil {
		return control.Result{}, err
	}
	if ticket.Type != control.TicketDeleteRepository && ticket.Type != control.TicketPrepareRepositoryRecovery {
		return control.Result{}, errors.New("not a resumable repository deletion ticket")
	}
	// Also bound bootstrap callers whose daemon context has no deadline.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	delay := 2 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return control.Result{}, err
		}
		result, err := transport.Exchange(ctx, ticket)
		if err == nil {
			if err := result.Validate(); err != nil {
				return control.Result{}, err
			}
			if result.OperationID != ticket.OperationID || result.RequestID != ticket.RequestID || result.Type != ticket.Type {
				return control.Result{}, errors.New("repository deletion response mismatch")
			}
			if err := validateDeletionTarget(ticket, result); err != nil {
				return control.Result{}, err
			}
			return result, nil
		}
		if ctx.Err() != nil || !interruptedControlTransport(err) {
			return control.Result{}, err
		}
		if waitErr := wait(ctx, delay); waitErr != nil {
			return control.Result{}, fmt.Errorf("repository deletion result still unconfirmed: %w", errors.Join(waitErr, err))
		}
		delay = min(2*delay, 30*time.Second)
	}
}

func validateDeletionTarget(ticket control.Ticket, result control.Result) error {
	if result.Status != control.ResultOK {
		return nil
	}
	if ticket.Type == control.TicketDeleteRepository {
		var request control.DeleteRepositoryPayload
		var receipt control.DeleteRepositoryResult
		if err := control.DecodePayload(ticket.Payload, &request); err != nil {
			return err
		}
		if err := control.DecodeResultPayload(result.Result, &receipt); err != nil {
			return err
		}
		if request.RepoID != receipt.RepoID {
			return errors.New("repository deletion receipt names another repository")
		}
		return nil
	}
	var request control.PrepareRepositoryRecoveryPayload
	var receipt control.PrepareRepositoryRecoveryResult
	if err := control.DecodePayload(ticket.Payload, &request); err != nil {
		return err
	}
	if err := control.DecodeResultPayload(result.Result, &receipt); err != nil {
		return err
	}
	if receipt.Manifest.OperationID != ticket.OperationID {
		return errors.New("repository recovery manifest names another operation")
	}
	for _, archive := range receipt.Manifest.Archives {
		if archive.RepoID != request.RepoID {
			return errors.New("repository recovery manifest names another repository")
		}
	}
	return nil
}

func waitDeletionReconnect(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
