package passport

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"filees/pkg/client"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/errcat"
	"github.com/google/uuid"
)

// LockIntent is persisted inside passports.json BEFORE any network mutation.
// Ticket and Comment are immutable; only Stage advances from prepare to locking.
type LockIntent struct {
	Stage    string          `json:"stage"`
	RepoID   string          `json:"repo_id"`
	ClientID string          `json:"client_id"`
	Path     string          `json:"path"`
	Mode     string          `json:"mode"`
	Metadata Metadata        `json:"metadata"`
	Ticket   *control.Ticket `json:"ticket,omitempty"`
}

type intentBackend interface {
	NewLockIntent(string, Metadata, string, time.Time) (LockIntent, error)
	ValidateLockIntent(string, LockIntent) error
	PrepareLockIntent(context.Context, string, LockIntent) error
	AcquireLockIntent(context.Context, string, LockIntent) (string, error)
	ConfirmLockIntent(context.Context, string, LockIntent) (*Lock, error)
}

// ControlSVNBackend is the durable Manager integration of conditional prepare.
// It requires the existing authenticated, pinned controlclient transport.
// No production starter selects it until its deployment gates are satisfied.
type ControlSVNBackend struct {
	SVNBackend
	RepoID, ClientID string
	Transport        controlclient.Exchanger
}

// Reject bypassing Manager's durable intent, even for a normal acquisition.
func (b ControlSVNBackend) Lock(context.Context, string, string, bool) (*Lock, string, error) {
	return nil, "", errors.New("control passport backend requires a durable lock intent")
}

func (b ControlSVNBackend) relative(path string) (string, error) {
	if !filepath.IsAbs(b.WC) || !filepath.IsAbs(path) {
		return "", errors.New("control passport requires absolute WC and target")
	}
	rel, err := filepath.Rel(b.WC, path)
	return filepath.ToSlash(rel), err
}

func (b ControlSVNBackend) NewLockIntent(path string, meta Metadata, mode string, now time.Time) (LockIntent, error) {
	rel, err := b.relative(path)
	if err != nil {
		return LockIntent{}, err
	}
	i := LockIntent{Stage: "prepare", RepoID: b.RepoID, ClientID: b.ClientID, Path: rel, Mode: mode, Metadata: meta}
	if mode != "acquire" {
		ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketPreparePassportReplacement, b.ClientID,
			control.PreparePassportReplacementPayload{RepoID: b.RepoID, Path: rel, ObservedLockID: meta.PreviousToken, PassportID: meta.PassportID, InstanceUID: meta.InstanceUID, Mode: mode}, now)
		if err != nil {
			return LockIntent{}, err
		}
		i.Ticket = &ticket
	}
	return i, b.ValidateLockIntent(path, i)
}

func (b ControlSVNBackend) ValidateLockIntent(path string, i LockIntent) error {
	rel, err := b.relative(path)
	if err != nil {
		return err
	}
	if b.Client == nil || i.RepoID != b.RepoID || i.ClientID != b.ClientID || i.Path != rel || (i.Stage != "prepare" && i.Stage != "locking") {
		return errors.New("passport intent binding mismatch")
	}
	if _, ok := b.Client.(client.LockReceiptReader); !ok {
		return errors.New("SVN client cannot confirm local lock possession")
	}
	meta := i.Metadata
	for _, id := range []string{i.ClientID, i.RepoID, meta.PassportID, meta.InstanceUID} {
		if _, err := uuid.Parse(id); err != nil {
			return err
		}
	}
	if meta.IssuedAt.IsZero() || !meta.ExpiresAt.After(meta.IssuedAt) || meta.HardExpiresAt.Before(meta.ExpiresAt) {
		return errors.New("invalid passport intent lifetime")
	}
	// Reuse the control contract's strict relative-path validation for fresh locks too.
	p := control.PreparePassportReplacementPayload{RepoID: i.RepoID, Path: rel, ObservedLockID: meta.PreviousToken, PassportID: meta.PassportID, InstanceUID: meta.InstanceUID, Mode: i.Mode}
	if i.Mode == "acquire" {
		if i.Ticket != nil || meta.PreviousToken != "" {
			return errors.New("fresh passport has replacement data")
		}
		p.Mode, p.ObservedLockID = "renew", "validation-only"
		return p.Validate()
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if i.Ticket == nil {
		return errors.New("passport replacement ticket missing")
	}
	if err := i.Ticket.Validate(); err != nil {
		return err
	}
	var payload control.PreparePassportReplacementPayload
	if err := control.DecodePayload(i.Ticket.Payload, &payload); err != nil {
		return err
	}
	if i.Ticket.Type != control.TicketPreparePassportReplacement || i.Ticket.ClientID != i.ClientID || payload != p {
		return errors.New("passport ticket does not match intent")
	}
	return nil
}

func (b ControlSVNBackend) PrepareLockIntent(ctx context.Context, path string, i LockIntent) error {
	if err := b.ValidateLockIntent(path, i); err != nil {
		return err
	}
	if i.Ticket == nil {
		return ctx.Err()
	}
	return controlclient.PreparePassportReplacement(ctx, b.Transport, *i.Ticket)
}

func (b ControlSVNBackend) AcquireLockIntent(ctx context.Context, path string, i LockIntent) (string, error) {
	if err := b.ValidateLockIntent(path, i); err != nil {
		return "", err
	}
	if i.Stage != "locking" {
		return "", errors.New("passport lock intent is not durable")
	}
	return b.Client.LockWithComment(ctx, b.WC, []string{path}, FormatComment(i.Metadata), false)
}

func (b ControlSVNBackend) ConfirmLockIntent(ctx context.Context, path string, i LockIntent) (*Lock, error) {
	if err := b.ValidateLockIntent(path, i); err != nil {
		return nil, err
	}
	proof, err := b.Client.(client.LockReceiptReader).ConfirmLock(ctx, b.WC, path, FormatComment(i.Metadata))
	if err != nil {
		return nil, err
	}
	if proof == nil || proof.Token == "" || proof.Owner != i.ClientID || proof.Comment != FormatComment(i.Metadata) {
		return nil, errcat.New(errcat.KeyPassportUncertain, nil, nil)
	}
	return &Lock{Token: proof.Token, Owner: proof.Owner, Comment: proof.Comment}, nil
}
