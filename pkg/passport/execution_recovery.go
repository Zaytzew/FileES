package passport

import (
	"context"
	"errors"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/errcat"
	"github.com/google/uuid"
)

// SettleLockIntent is valid only after the client has persisted the closing
// direction. It is independent of local expiry and never retries SVN lock.
func (b ControlSVNBackend) SettleLockIntent(ctx context.Context, path string, i LockIntent) error {
	if err := b.ValidateLockIntent(path, i); err != nil {
		return err
	}
	if i.Acquisition == nil || (i.Stage != "settling" && i.Stage != "canceling") {
		return errors.New("acquisition settlement is not durable")
	}
	ticket := *i.Acquisition
	ticket.Type = control.TicketSettlePassportAcquisition
	return controlclient.PassportExecution(ctx, b.Transport, ticket)
}

func (m *Manager) settlePending(ctx context.Context, p Passport) (Passport, string, error) {
	b, ok := m.backend.(interface {
		SettleLockIntent(context.Context, string, LockIntent) error
	})
	if !ok || p.Pending == nil || p.Pending.Acquisition == nil {
		return p, "", errcat.New(errcat.KeyPassportUncertain, nil, nil)
	}
	i := *p.Pending
	if i.Stage != "locking" && i.Stage != "checking" && i.Stage != "settling" {
		return p, "", errors.New("invalid acquisition settlement stage")
	}
	i.Stage = "settling"
	p.Pending = &i
	m.passports[p.Path] = p
	m.publishPendingLocked()
	if err := m.saveLocked(); err != nil {
		return p, "", err
	}
	if err := b.SettleLockIntent(ctx, p.Path, i); err != nil {
		return p, "", err
	}
	return p, "", m.retirePending(p, errcat.New(errcat.KeyPassportAborted, nil, nil))
}

// Inspection gives the server an opportunity to reap a declared passport
// expiry before the local owner/guest decision. A client clock cannot force it.
func (b ControlSVNBackend) Inspect(ctx context.Context, path string) (*Lock, error) {
	lock, err := b.SVNBackend.Inspect(ctx, path)
	if err != nil || lock == nil || !b.FenceAcquisitions {
		return lock, err
	}
	if _, ok := ParseComment(lock.Comment); !ok {
		return lock, nil
	}
	rel, err := b.relative(path)
	if err != nil {
		return nil, err
	}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketExpirePassportPath, b.ClientID, control.PassportExecutionPayload{RepoID: b.RepoID, Path: rel}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := controlclient.PassportExecution(ctx, b.Transport, ticket); err != nil {
		return nil, err
	}
	return b.SVNBackend.Inspect(ctx, path)
}
