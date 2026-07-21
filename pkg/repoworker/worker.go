// Package repoworker executes authenticated repository control requests.
// Identity and authority are supplied by the forced-command session, never by
// the ticket payload.
package repoworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

type Session struct {
	ClientID              string
	RealmID               string
	CanCreateRepositories bool
}

func (s Session) Validate() error {
	if strings.TrimSpace(s.ClientID) == "" {
		return errors.New("authenticated client ID is required")
	}
	if _, err := uuid.Parse(s.RealmID); err != nil {
		return errors.New("authenticated realm ID must be UUID")
	}
	return nil
}

type Repository struct{ RepoID, URL string }

// Backend owns the durable server transaction: canonical record, FSFS, owner
// grant, authz and projection. Create must itself be resumable by operation ID.
type Backend interface {
	Create(context.Context, string, string, string) (Repository, error) // operation, realm, name
}

type ResultStore interface {
	Load(operationID string, typ control.TicketType) (control.Result, bool, error)
	Save(control.Result) error
}

type RepositoryActivator interface {
	Activate(context.Context, string, string) error // repo ID, authenticated realm ID
}

type Worker struct {
	Backend   Backend
	Activator RepositoryActivator
	Store     ResultStore
	Now       func() time.Time
}

func (w *Worker) Handle(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if err := session.Validate(); err != nil {
		return control.Result{}, err
	}
	if err := ticket.Validate(); err != nil {
		return control.Result{}, err
	}
	if ticket.ClientID != session.ClientID {
		return control.Result{}, errors.New("ticket client does not match authenticated session")
	}
	if ticket.Type != control.TicketCreateRepository && ticket.Type != control.TicketInitialCommit {
		return control.Result{}, errors.New("unsupported repository worker ticket")
	}
	if ticket.Type == control.TicketCreateRepository && !session.CanCreateRepositories {
		return w.failure(ticket, "CREATE_REPOSITORY_FORBIDDEN", "authenticated session cannot create repositories")
	}
	if w.Store == nil {
		return control.Result{}, errors.New("repository result store is required")
	}
	if result, ok, err := w.Store.Load(ticket.OperationID, ticket.Type); err != nil {
		return control.Result{}, err
	} else if ok {
		if result.RequestID != ticket.RequestID {
			return control.Result{}, errors.New("operation already bound to another request")
		}
		return result, nil
	}
	if ticket.Type == control.TicketInitialCommit {
		return w.activate(ctx, session, ticket)
	}
	var payload control.CreateRepositoryPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	if w.Backend == nil {
		return control.Result{}, errors.New("repository backend is required")
	}
	repo, err := w.Backend.Create(ctx, ticket.OperationID, session.RealmID, strings.TrimSpace(payload.Name))
	if err != nil {
		// Backend boundaries are resumable. Report the current attempt but do not
		// bind a terminal result: the same operation/request must be retryable.
		return control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: "CREATE_REPOSITORY_RETRY", Message: err.Error()}, w.now())
	}
	if strings.TrimSpace(repo.RepoID) == "" || strings.TrimSpace(repo.URL) == "" {
		return control.Result{}, fmt.Errorf("backend returned incomplete repository")
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.CreateRepositoryResult{RepoID: repo.RepoID, RepoURL: repo.URL}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) activate(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	var payload control.InitialCommitPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	if w.Activator == nil {
		return control.Result{}, errors.New("repository activator is required")
	}
	if err := w.Activator.Activate(ctx, payload.RepoID, session.RealmID); err != nil {
		return control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: "INITIAL_COMMIT_RETRY", Message: err.Error()}, w.now())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.InitialCommitResult{Acknowledged: true}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) failure(t control.Ticket, code, message string) (control.Result, error) {
	r, e := control.NewErrorResult(t.OperationID, t.RequestID, t.Type, control.ErrorBody{Code: code, Message: message}, w.now())
	if e == nil {
		if w.Store == nil {
			return control.Result{}, errors.New("repository result store is required")
		}
		e = w.Store.Save(r)
	}
	return r, e
}
func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
