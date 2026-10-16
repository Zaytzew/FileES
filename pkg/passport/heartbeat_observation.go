package passport

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// At most one ownership read per heartbeat, loaded only if a passport actually
// needs an observation. Never persist its freshness or use it for a mutation.
// A failed emission is not an empty list and must not fan out into N retries.
func (m *Manager) heartbeatInspector(ctx context.Context) func(Passport) (*Lock, error) {
	var view OwnershipView
	var readErr error
	loaded := false
	return func(p Passport) (*Lock, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := m.cfg.Now().UTC()
		mutating := (!p.CloseAfter.IsZero() && !now.Before(p.CloseAfter)) ||
			!now.Before(p.HardExpiresAt) || !now.Add(m.cfg.HeartbeatInterval).Before(p.ExpiresAt)
		if m.cfg.HeartbeatOwnership == nil || mutating {
			return m.backend.Inspect(ctx, p.Path)
		}
		if !loaded {
			view, readErr = m.cfg.HeartbeatOwnership(ctx)
			loaded = true
		}
		if readErr != nil {
			return nil, readErr
		}
		// Include time spent obtaining the view and processing earlier paths.
		now = m.cfg.Now().UTC()
		if view.ObservedAt.IsZero() || now.Before(view.ObservedAt) || now.Sub(view.ObservedAt) >= time.Minute {
			return nil, errors.New("passport heartbeat: state observation is not fresh")
		}
		if !view.ObservedAt.After(p.LastHeartbeatAt) || !view.ObservedAt.After(p.IssuedAt) {
			// A state request started before our lock/renewal cannot disprove it.
			return m.backend.Inspect(ctx, p.Path)
		}
		if m.cfg.WorkingCopy == "" {
			return nil, errors.New("passport heartbeat: working copy binding missing")
		}
		rel, err := filepath.Rel(m.cfg.WorkingCopy, p.Path)
		if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("passport heartbeat: path outside working copy")
		}
		hold, ok := view.Holds[filepath.ToSlash(rel)]
		if !ok {
			return nil, nil // complete, fresh server emission confirms absence
		}
		if hold.Token == "" || hold.Owner == "" || hold.Comment == "" {
			return nil, errors.New("passport heartbeat: incomplete lock observation")
		}
		return &Lock{Token: hold.Token, Owner: hold.Owner, Comment: hold.Comment}, nil
	}
}
