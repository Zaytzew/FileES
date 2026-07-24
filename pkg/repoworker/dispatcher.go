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

const MaxTicketBytes = 64 << 10

type SessionResolver interface {
	Resolve(clientID string) (Session, error)
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
	Worker   *Worker
	Resolver SessionResolver
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
		return errors.New("control ticket exceeds limit")
	}
	ticket, e := control.ParseTicket(bytes.TrimSpace(raw))
	if e != nil {
		return fmt.Errorf("parse control ticket: %w", e)
	}
	session, e := d.Resolver.Resolve(clientID)
	if e != nil {
		return e
	}
	result, e := d.Worker.Handle(ctx, session, ticket)
	if e != nil {
		return e
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}
