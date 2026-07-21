package provisioning

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	control "filees/pkg/control/v1"
)

type ControlExchange interface {
	Exchange(context.Context, control.Ticket) (control.Result, error)
}

type Orchestrator struct {
	Store   *Store
	Control ControlExchange
	SVN     InitialSVN
	Limits  ImportLimits
}

// RunCreate advances one operation through every durable boundary. It is safe
// to call after a daemon restart; pending request IDs and completed SVN paths
// are reused from durable state.
func (o Orchestrator) RunCreate(ctx context.Context, operationID string) (Operation, error) {
	if o.Store == nil || o.Control == nil || o.SVN == nil {
		return Operation{}, errors.New("repository provisioning orchestrator is incomplete")
	}
	for {
		if err := ctx.Err(); err != nil {
			return Operation{}, err
		}
		op, err := o.Store.Get(operationID)
		if err != nil {
			return Operation{}, err
		}
		switch op.State {
		case StateLocalValidated, StateRepositoryRequestFailed:
			if _, err := o.Store.RequestRepository(operationID, uuid.NewString()); err != nil {
				return Operation{}, err
			}
		case StateProvisioningRequested:
			requestID, err := pendingRequest(op, control.TicketCreateRepository)
			if err != nil {
				return Operation{}, err
			}
			ticket, err := o.Store.CreateRepositoryTicket(operationID, requestID)
			if err != nil {
				return Operation{}, err
			}
			result, err := o.Control.Exchange(ctx, ticket)
			if err != nil {
				return Operation{}, fmt.Errorf("exchange CREATE_REPOSITORY: %w", err)
			}
			updated, err := o.Store.ApplyRepositoryResult(result)
			if err != nil {
				return Operation{}, err
			}
			if updated.State == StateRepositoryRequestFailed {
				return updated, requestFailure(updated, requestID)
			}
		case StateRepositoryReady, StateInitialCommitFailed:
			if _, err := o.Store.StartInitialCommit(operationID, uuid.NewString()); err != nil {
				return Operation{}, err
			}
		case StateInitialCommitInProgress:
			requestID, err := pendingRequest(op, control.TicketInitialCommit)
			if err != nil {
				return Operation{}, err
			}
			if _, err := PublishInitialSnapshot(ctx, o.Store, operationID, requestID, o.SVN, o.Limits); err != nil {
				return Operation{}, err
			}
		case StateInitialSnapshotPublished:
			requestID, err := pendingRequest(op, control.TicketInitialCommit)
			if err != nil {
				return Operation{}, err
			}
			ticket, err := o.Store.InitialCommitTicket(operationID, requestID)
			if err != nil {
				return Operation{}, err
			}
			result, err := o.Control.Exchange(ctx, ticket)
			if err != nil {
				return Operation{}, fmt.Errorf("exchange INITIAL_COMMIT: %w", err)
			}
			updated, err := o.Store.ApplyInitialCommitResult(result)
			if err != nil {
				return Operation{}, err
			}
			if updated.State == StateInitialCommitFailed {
				return updated, requestFailure(updated, requestID)
			}
		case StateActive:
			return op, nil
		default:
			return Operation{}, fmt.Errorf("unsupported create provisioning state %q", op.State)
		}
	}
}

func pendingRequest(op Operation, typ control.TicketType) (string, error) {
	ids := make([]string, 0)
	for requestID, record := range op.Requests {
		if record.Type == typ && record.Status == RequestPending {
			ids = append(ids, requestID)
		}
	}
	sort.Strings(ids)
	if len(ids) != 1 {
		return "", fmt.Errorf("state %s requires exactly one pending %s request, got %d", op.State, typ, len(ids))
	}
	return ids[0], nil
}

func requestFailure(op Operation, requestID string) error {
	record := op.Requests[requestID]
	if record.Error == nil {
		return errors.New("repository control request failed without error details")
	}
	return fmt.Errorf("%s: %s", record.Error.Code, record.Error.Message)
}
