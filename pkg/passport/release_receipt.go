package passport

import (
	"context"
	"errors"
	"time"

	"filees/pkg/client"
)

const StateReleasing = "releasing"

type releaseReceiptBackend interface {
	ReleaseToken(context.Context, string, string) (*client.LockReleaseReceipt, string, error)
}

func (b SVNBackend) ReleaseToken(ctx context.Context, path, token string) (*client.LockReleaseReceipt, string, error) {
	if c, ok := b.Client.(client.LockReleaser); ok {
		return c.ReleaseLock(ctx, b.WC, path, token)
	}
	return nil, "", nil
}

// The already persisted fencing token is the immutable release intent. An
// uncertain response leaves it releasing; restart retries this token only.
// No cache observation grants permission or substitutes another token.
func (m *Manager) releaseWithReceipt(ctx context.Context, p Passport, b releaseReceiptBackend) (string, error) {
	defer m.publishPendingLocked()
	if p.State != StateReleasing {
		p.State = StateReleasing
		p.CloseAfter = time.Time{}
		m.passports[p.Path] = p
	}
	// Repeat persistence even after a prior failed save. No remote mutation may
	// precede a durable intent, including a retry in this same process.
	if err := m.saveLocked(); err != nil {
		return "", err
	}
	proof, out, err := b.ReleaseToken(ctx, p.Path, p.FencingToken)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if proof == nil {
		// Explicit unsupported helper: no mutation was attempted. Preserve the
		// legacy live checks, including on recovery after upgrading/downgrading.
		current, err := m.backend.Inspect(ctx, p.Path)
		if err != nil {
			return out, err
		}
		if !owns(p, current) {
			p.State = StateLost
			m.passports[p.Path] = p
			return out, errors.Join(ErrPassportLost, m.saveLocked())
		}
		out, err = m.backend.Unlock(ctx, p.Path)
		if err != nil {
			return out, err
		}
	} else if proof.Token != p.FencingToken || (proof.State != "released" && proof.State != "not-held") {
		return out, errors.New("passport release receipt mismatch")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	delete(m.passports, p.Path)
	if err := m.saveLocked(); err != nil {
		m.passports[p.Path] = p // retain the same intent in memory as on disk
		return out, err
	}
	if m.cfg.OnReleased != nil {
		m.cfg.OnReleased(p.Path, p.FencingToken)
	}
	return out, nil
}
