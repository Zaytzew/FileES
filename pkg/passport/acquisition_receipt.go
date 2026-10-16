package passport

import (
	"context"
	"errors"
	"time"

	"filees/pkg/client"
)

func acquisitionReceiptFresh(started, now time.Time) bool {
	return !started.IsZero() && !now.Before(started) && now.Sub(started) < time.Minute
}

// Used only on the stack of a new acquisition. No persisted receipt may skip
// recovery after a crash or be treated as a fresh ownership observation.
type acquisitionReceiptBackend interface {
	AcquireLockIntentReceipt(context.Context, string, LockIntent) (*Lock, string, error)
}

func (b ControlSVNBackend) AcquireLockIntentReceipt(ctx context.Context, path string, i LockIntent) (*Lock, string, error) {
	if err := b.ValidateLockIntent(path, i); err != nil {
		return nil, "", err
	}
	if i.Stage != "locking" {
		return nil, "", errors.New("passport lock intent is not durable")
	}
	c, ok := b.Client.(client.LockAcquirer)
	if !ok {
		out, err := b.AcquireLockIntent(ctx, path, i)
		return nil, out, err
	}
	proof, out, err := c.AcquireLock(ctx, b.WC, path, FormatComment(i.Metadata))
	if err != nil {
		return nil, out, err
	}
	if err = ctx.Err(); err != nil {
		return nil, out, err
	}
	if proof == nil {
		return nil, out, nil
	}
	if proof.Token == "" || proof.Owner != i.ClientID || proof.Comment != FormatComment(i.Metadata) {
		return nil, out, errors.New("passport acquisition receipt binding mismatch")
	}
	return &Lock{Token: proof.Token, Owner: proof.Owner, Comment: proof.Comment}, out, nil
}
