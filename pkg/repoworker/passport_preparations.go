package repoworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
)

// PassportPreparations adds durable request binding around the existing
// conditional authority. Production also holds worker/service-WC locks and
// realm-removal admission across the call. It never acquires the new lock.
type PassportPreparations struct {
	Root      string
	Authority PassportReplacementAuthority
}

type passportPreparationRecord struct {
	Schema      string          `json:"schema"`
	Digest      string          `json:"digest"`
	State       string          `json:"state"`
	Result      *control.Result `json:"result,omitempty"`
	PriorResult *control.Result `json:"prior_result,omitempty"`
}

func preparationDigest(session Session, ticket control.Ticket) string {
	raw, _ := json.Marshal(struct {
		RealmID string         `json:"realm_id"`
		Ticket  control.Ticket `json:"ticket"`
	}{session.RealmID, ticket})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func preparationError(ticket control.Ticket, key errcat.Key, now time.Time) (control.Result, error) {
	spec, _ := errcat.ByKey(key)
	return control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type,
		control.ErrorBody{Code: string(spec.Code), Message: string(spec.Key)}, now)
}

func (s PassportPreparations) Handle(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if err := session.Validate(); err != nil {
		return control.Result{}, err
	}
	if err := ticket.Validate(); err != nil {
		return control.Result{}, err
	}
	if ticket.Type != control.TicketPreparePassportReplacement || ticket.ClientID != session.ClientID {
		return control.Result{}, errors.New("passport ticket does not match authenticated session")
	}
	var p control.PreparePassportReplacementPayload
	if err := control.DecodePayload(ticket.Payload, &p); err != nil {
		return control.Result{}, err
	}
	now := time.Now
	if s.Authority.Now != nil {
		now = s.Authority.Now
	}
	failure := func(key errcat.Key) (control.Result, error) { return preparationError(ticket, key, now()) }
	if err := s.Authority.authorizeRequester(session, p.RepoID); err != nil {
		return failure(errcat.KeyPassportDenied)
	}
	if !filepath.IsAbs(s.Root) {
		return failure(errcat.KeyPassportUnavailable)
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return control.Result{}, err
	}
	// Persist creation of the receipt directory as well as individual files.
	if err := syncDirectory(filepath.Dir(s.Root)); err != nil {
		return control.Result{}, err
	}
	var result control.Result
	err := WithFileLock(filepath.Join(s.Root, ticket.OperationID+".lock"), func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(s.Root, ticket.OperationID+".json")
		digest := preparationDigest(session, ticket)
		raw, err := os.ReadFile(path)
		if err == nil {
			var record passportPreparationRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			if record.Schema != "filees.passport-preparation/v1" {
				return errors.New("invalid passport preparation record")
			}
			if record.Digest != digest {
				result, err = failure(errcat.KeyPassportRequestConflict)
				return err
			}
			switch record.State {
			case "started":
				// We hold the operation lock: the previous handler has ended, but
				// its conditional old-token release may or may not have completed.
				// Never replay it or claim success. Seal a terminal refusal BEFORE
				// replying so this ticket can never authorize a subsequent acquire.
				// A surviving svnadmin child can only target the exact OLD token.
				result, err = failure(errcat.KeyPassportAborted)
				if err != nil {
					return err
				}
				record.State, record.Result = "finished", &result
				return atomicJSON(path, record)
			case "finished", "canceled":
				if record.Result == nil {
					return errors.New("passport preparation receipt missing")
				}
				result = *record.Result
				if record.State == "canceled" && (result.Error == nil || result.Error.Code != string(errcat.CodePassportAborted) || result.Error.Message != string(errcat.KeyPassportAborted)) {
					return errors.New("invalid cancellation tombstone")
				}
				if err := result.Validate(); err != nil {
					return err
				}
				if result.OperationID != ticket.OperationID || result.RequestID != ticket.RequestID || result.Type != ticket.Type {
					return errors.New("passport preparation receipt mismatch")
				}
				if result.Status == control.ResultOK {
					var receipt control.PreparePassportReplacementResult
					if err := control.DecodePayload(result.Result, &receipt); err != nil {
						return err
					}
					if receipt.RepoID != p.RepoID || receipt.Path != p.Path || receipt.ObservedLockID != p.ObservedLockID {
						return errors.New("passport receipt payload mismatch")
					}
				}
				return nil
			default:
				return errors.New("invalid passport preparation state")
			}
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		record := passportPreparationRecord{Schema: "filees.passport-preparation/v1", Digest: digest, State: "started"}
		if err := atomicJSON(path, record); err != nil {
			return err
		}
		// No mutation is allowed before the intent is durable.
		err = s.Authority.Prepare(ctx, session, PassportReplacement{
			RepoID: p.RepoID, Path: p.Path, ObservedToken: p.ObservedLockID,
			PassportID: p.PassportID, InstanceUID: p.InstanceUID, Mode: p.Mode,
		})
		switch {
		case err == nil:
			result, err = control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type,
				control.PreparePassportReplacementResult{RepoID: p.RepoID, Path: p.Path, ObservedLockID: p.ObservedLockID, State: "prepared"}, now())
		case errors.Is(err, ErrPassportReplacementDenied):
			result, err = failure(errcat.KeyPassportDenied)
		case errors.Is(err, ErrPassportReplacementStale):
			result, err = failure(errcat.KeyPassportStale)
		case errors.Is(err, ErrPathOwnerUnavailable):
			result, err = failure(errcat.KeyPathOwnerUnavailable)
		default:
			// Unknown errors may follow an effective unlock. Keep "started" so a
			// reconnect cannot repeat the mutation; retain the diagnostic in stderr.
			return err
		}
		if err != nil {
			return err
		}
		record.State, record.Result = "finished", &result
		return atomicJSON(path, record)
	})
	return result, err
}
