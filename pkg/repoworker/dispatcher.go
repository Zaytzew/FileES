package repoworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"fmt"
	"io"
	"path/filepath"
)

const MaxTicketBytes = control.MaxShareTicketBytes

type SessionResolver interface {
	Resolve(clientID string) (Session, error)
}

// SessionAdmission is evaluated after both the authenticated session and the
// signed ticket have been resolved, but before any worker replay or mutation.
// Production uses it to keep a realm-removal fence effective after a worker
// crash releases the global worker lock.
type SessionAdmission interface {
	Admit(Session, control.Ticket) error
}

type ViewResolver struct{ ServiceWC string }

func (r ViewResolver) Resolve(clientID string) (Session, error) {
	if !filepath.IsAbs(r.ServiceWC) {
		return Session{}, errors.New("service working copy must be absolute")
	}
	v, e := clientview.Load(filepath.Join(r.ServiceWC, "clients", clientID, "view.json"))
	if e != nil {
		return Session{}, e
	}
	if v.ClientID != clientID {
		return Session{}, errors.New("client view identity mismatch")
	}
	return Session{ClientID: v.ClientID, RealmID: v.RealmID, CanCreateRepositories: v.CanCreateRepositories(), Repositories: v.Repositories}, nil
}

type Dispatcher struct {
	Worker    *Worker
	Resolver  SessionResolver
	Admission SessionAdmission
}

func (d Dispatcher) Serve(ctx context.Context, clientID string, in io.Reader, out io.Writer) error {
	if d.Worker == nil || d.Resolver == nil {
		return errors.New("repository dispatcher is incomplete")
	}
	raw, e := io.ReadAll(io.LimitReader(in, MaxTicketBytes+1))
	if e != nil {
		return e
	}
	if len(raw) > MaxTicketBytes {
		return control.ErrTicketTooLarge
	}
	// Read only the routing field before full validation. A larger buffer is
	// not permission to send oversized ordinary operations to the worker.
	var envelope struct {
		Type control.TicketType `json:"type"`
	}
	if e := json.Unmarshal(raw, &envelope); e != nil {
		return fmt.Errorf("parse control envelope: %w", e)
	}
	if len(raw) > control.TicketByteLimit(envelope.Type) {
		return control.ErrTicketTooLarge
	}
	ticket, e := control.ParseTicket(bytes.TrimSpace(raw))
	if e != nil {
		return fmt.Errorf("parse control ticket: %w", e)
	}
	session, e := d.Resolver.Resolve(clientID)
	if e != nil {
		return e
	}
	if d.Admission != nil {
		if e := d.Admission.Admit(session, ticket); e != nil {
			return e
		}
	}
	result, e := d.Worker.Handle(ctx, session, ticket)
	if e != nil {
		return e
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}
