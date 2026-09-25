package passport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/errcat"
)

type OwnershipHold struct{ Token, RealmID string }
type OwnershipView struct {
	Owners map[string]string
	Holds  map[string]OwnershipHold
}

func (m *Manager) PerPathOwnership() bool { return m.cfg.Ownership != nil }

// AcquireOwned borrows only the caller's own objects. A non-owner may publish
// using an already acquired explicit passport, never by an implicit borrow.
func (m *Manager) AcquireOwned(ctx context.Context, paths []string, realmID string) error {
	if m.cfg.Ownership == nil || realmID == "" {
		return errcat.New(errcat.KeyPathOwnerUnavailable, nil, nil)
	}
	view, err := m.cfg.Ownership(ctx)
	if err != nil {
		return err
	}
	local := map[string]Passport{}
	for _, p := range m.Snapshot() {
		local[p.Path] = p
	}
	for _, p := range cleanPaths(paths) {
		if current, ok := local[p]; ok && current.State == StateActive {
			continue
		} // BeginPublish verifies fencing
		rel, err := filepath.Rel(m.cfg.WorkingCopy, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("passport path outside working copy")
		}
		owner := view.Owners[filepath.ToSlash(rel)]
		if owner == "" {
			return errcat.New(errcat.KeyPathOwnerUnavailable, nil, nil)
		}
		if owner != realmID {
			return ErrNoPassport
		}
		if _, _, err := m.Acquire(ctx, []string{p}, realmID); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile BOTH directions. A lost/unknown owner or a foreign hold removes
// speculative RW; local bytes are never discarded. A confirmed local passport
// is independent of ownership (explicit borrowing by a non-owner).
func (m *Manager) reconcilePathAccess(ctx context.Context, wc, realmID string) error {
	source, ok := m.backend.(interface {
		NeedsLockPaths(context.Context, string) (map[string]bool, error)
	})
	if !ok {
		return errors.New("cannot list needs-lock paths")
	}
	candidates, err := source.NeedsLockPaths(ctx, wc)
	if err != nil {
		return err
	}
	view, observationErr := m.cfg.Ownership(ctx)
	local := map[string]Passport{}
	for _, p := range m.Snapshot() {
		local[p.Path] = p
	}
	walkErr := filepath.WalkDir(wc, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".svn" || d.Name() == ".filees" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(wc, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !candidates[rel] {
			return nil
		}
		// A confirmed, unexpired passport keeps its file writable whatever the
		// observation says, unless the observation shows another realm holding
		// it. Owner's production, 2026-09-25 (KRAŃCOWA-PŁOŃSK): after each of
		// his own commits the view was unknown until the server's projection
		// reached the new revision, and after each heartbeat it still carried
		// the previous token - both removed write access from the drawing he
		// held and had open, and BricsCAD refused to save it as read-only.
		// Losing the lock itself is detected by heartbeat and publication, which
		// inspect the repository, not by this projection.
		pass, hasPass := local[p]
		if hasPass && pass.State == StatePending && pass.Pending != nil && pass.Pending.Mode == "renew" {
			return nil // renewing a held passport: neither grant nor revoke meanwhile
		}
		hold, held := view.Holds[rel]
		rw := false
		if hasPass && pass.State == StateActive && m.cfg.Now().Before(pass.ExpiresAt) {
			foreign := observationErr == nil && held && hold.Token != pass.FencingToken && hold.RealmID != pass.RealmID && hold.RealmID != realmID
			rw = !foreign
		}
		if observationErr == nil && (!hasPass || pass.State != StatePending) && realmID != "" && view.Owners[rel] == realmID && (!held || hold.RealmID == realmID) {
			rw = true
		}
		mode := info.Mode().Perm() &^ 0222
		if rw {
			mode = info.Mode().Perm() | 0200
		}
		if mode != info.Mode().Perm() {
			return os.Chmod(p, mode)
		}
		return nil
	})
	return errors.Join(observationErr, walkErr)
}
