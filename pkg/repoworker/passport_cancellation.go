package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
)

// Cancel fences the original preparation under its existing operation lock.
// It never calls SVN and cannot settle an acquisition sent on the SVN channel.
func (s PassportPreparations) Cancel(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if err := session.Validate(); err != nil {
		return control.Result{}, err
	}
	if err := ticket.Validate(); err != nil {
		return control.Result{}, err
	}
	if ticket.Type != control.TicketCancelPassportPreparation || ticket.ClientID != session.ClientID {
		return control.Result{}, errors.New("cancellation does not match session")
	}
	var payload control.CancelPassportPreparationPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	original := payload.Preparation
	var p control.PreparePassportReplacementPayload
	if err := control.DecodePayload(original.Payload, &p); err != nil {
		return control.Result{}, err
	}
	now := time.Now
	if s.Authority.Now != nil {
		now = s.Authority.Now
	}
	failure := func(key errcat.Key) (control.Result, error) { return preparationError(ticket, key, now()) }
	// Cancellation cannot mutate SVN or grant access. An active actor can
	// fence its own bound preparation after the repository grant was revoked.
	if err := s.Authority.authorizePassportCleanup(session); err != nil {
		return failure(errcat.KeyPassportDenied)
	}
	if !filepath.IsAbs(s.Root) {
		return failure(errcat.KeyPassportUnavailable)
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return control.Result{}, err
	}
	if err := syncDirectory(filepath.Dir(s.Root)); err != nil {
		return control.Result{}, err
	}
	conflict := false
	err := WithFileLock(filepath.Join(s.Root, original.OperationID+".lock"), func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(s.Root, original.OperationID+".json")
		digest := preparationDigest(session, original)
		record := passportPreparationRecord{Schema: "filees.passport-preparation/v1", Digest: digest}
		raw, err := os.ReadFile(path)
		if err == nil {
			// Missing fields in an existing record must not inherit defaults.
			record = passportPreparationRecord{}
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			if record.Schema != "filees.passport-preparation/v1" {
				return errors.New("invalid preparation record")
			}
			if record.Digest != digest {
				conflict = true
				return nil
			}
			switch record.State {
			case "started":
				if record.Result != nil {
					return errors.New("started preparation has a receipt")
				}
			case "finished", "canceled":
				if record.Result == nil {
					return errors.New("preparation receipt missing")
				}
				r := record.Result
				if err := r.Validate(); err != nil {
					return err
				}
				if r.OperationID != original.OperationID || r.RequestID != original.RequestID || r.Type != original.Type {
					return errors.New("preparation receipt mismatch")
				}
				if r.Status == control.ResultOK {
					var receipt control.PreparePassportReplacementResult
					if err := control.DecodePayload(r.Result, &receipt); err != nil {
						return err
					}
					if receipt.RepoID != p.RepoID || receipt.Path != p.Path || receipt.ObservedLockID != p.ObservedLockID {
						return errors.New("preparation receipt payload mismatch")
					}
				}
				if record.State == "canceled" {
					if r.Error == nil || r.Error.Code != string(errcat.CodePassportAborted) || r.Error.Message != string(errcat.KeyPassportAborted) {
						return errors.New("invalid cancellation tombstone")
					}
					return nil
				}
				record.PriorResult = record.Result
			default:
				return errors.New("invalid preparation state")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		aborted, err := preparationError(original, errcat.KeyPassportAborted, now())
		if err != nil {
			return err
		}
		record.State, record.Result = "canceled", &aborted
		return atomicJSON(path, record)
	})
	if err != nil {
		return control.Result{}, err
	}
	if conflict {
		return failure(errcat.KeyPassportRequestConflict)
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.CancelPassportPreparationResult{PreparationOperationID: original.OperationID, PreparationRequestID: original.RequestID, State: "canceled"}, now())
}
