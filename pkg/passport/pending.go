package passport

import (
	"context"
	"errors"
	"time"

	"filees/pkg/errcat"
)

const StatePending = "pending"

// beginPending and resumePending run under Manager's existing opMu and mu.
func (m *Manager) beginPending(ctx context.Context, path string, meta Metadata, mode string, closeAfter time.Time) (Passport, string, error) {
	i, err := m.backend.(intentBackend).NewLockIntent(path, meta, mode, m.cfg.Now().UTC())
	if err != nil {
		return Passport{}, "", err
	}
	p := Passport{Path: path, PassportID: meta.PassportID, InstanceUID: meta.InstanceUID, RealmID: meta.RealmID, IssuedAt: meta.IssuedAt, ExpiresAt: meta.ExpiresAt, HardExpiresAt: meta.HardExpiresAt, CloseAfter: closeAfter, State: StatePending, Pending: &i}
	m.passports[path] = p
	// On a failed save retain pending in memory. A later retry MUST save again
	// before prepare; it cannot mint a different request or roll back a maybe-write.
	return m.resumePending(ctx, p)
}

func (m *Manager) resumePending(ctx context.Context, p Passport) (Passport, string, error) {
	b, ok := m.backend.(intentBackend)
	if !ok || p.Pending == nil {
		return p, "", errors.New("passport pending backend unavailable")
	}
	i := *p.Pending
	if err := b.ValidateLockIntent(p.Path, i); err != nil {
		return p, "", err
	}
	checkLive := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := m.cfg.Now().UTC()
		if !now.Before(i.Metadata.ExpiresAt) || !now.Before(i.Metadata.HardExpiresAt) {
			return errcat.New(errcat.KeyPassportUncertain, nil, nil)
		}
		return nil
	}
	if err := checkLive(); err != nil {
		return p, "", err
	}
	var out string
	if i.Stage == "prepare" {
		if err := m.saveLocked(); err != nil {
			return p, "", err
		}
		if err := b.PrepareLockIntent(ctx, p.Path, i); err != nil {
			return p, "", err
		}
		i.Stage = "locking"
		p.Pending = &i
		m.passports[p.Path] = p
		if err := m.saveLocked(); err != nil {
			return p, "", err
		}
		if err := checkLive(); err != nil {
			return p, "", err
		}
		var err error
		out, err = b.AcquireLockIntent(ctx, p.Path, i)
		if err != nil {
			return p, out, err
		}
	}
	// After a restart in locking, never run lock again. Only a WC+server receipt
	// can resolve a lost reply; neither an absent lock nor a copied comment can.
	lock, err := b.ConfirmLockIntent(ctx, p.Path, i)
	if err != nil {
		return p, out, err
	}
	if lock == nil || lock.Token == "" {
		return p, out, errcat.New(errcat.KeyPassportUncertain, nil, nil)
	}
	if err := checkLive(); err != nil {
		return p, out, err
	}
	confirmed := p
	confirmed.Pending, confirmed.State, confirmed.FencingToken = nil, StateActive, lock.Token
	confirmed.LastHeartbeatAt = m.cfg.Now().UTC()
	m.passports[p.Path] = confirmed
	if err := m.saveLocked(); err != nil {
		m.passports[p.Path] = p // keep the recovery fence until a successful save
		return p, out, err
	}
	return confirmed, out, nil
}
