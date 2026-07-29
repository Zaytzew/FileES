package repoworker

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/onboarding"
	"github.com/google/uuid"
)

// RealmRemovalStore persists the destructive-operation context separately
// from onboarding.  It intentionally contains no activation capability.
type RealmRemovalStore struct {
	Root      string
	OTPPepper []byte
	TTL       time.Duration
	Attempts  int
	Now       func() time.Time
}
type RealmRemovalState string

const (
	RealmRemovalAwaitingConfirmation RealmRemovalState = "awaiting_confirmation"
	RealmRemovalDeleting             RealmRemovalState = "deleting"
	RealmRemovalRecoveryReady        RealmRemovalState = "recovery_ready"
	RealmRemovalRevokingClients      RealmRemovalState = "revoke_all_clients"
	RealmRemovalCompleted            RealmRemovalState = "completed"
	RealmRemovalExpired              RealmRemovalState = "expired"
)

type RealmRemovalScope struct{ ClientIDs, OwnedRepoIDs, ForeignGrantRepoIDs []string }
type RealmRemovalRequest struct {
	NotificationEmail string `json:"notification_email"`
	ErasureRequested  bool   `json:"erasure_requested"`
	RecoveryPublicKey string `json:"recovery_public_key"`
}

// RealmRemovalMailState is separate from the irreversible removal state:
// SMTP acceptance does not establish confirmation, and confirmation must not
// wait for a later mail retry.
type RealmRemovalMailState string

const (
	RealmRemovalMailPending  RealmRemovalMailState = "pending"
	RealmRemovalMailSending  RealmRemovalMailState = "sending"
	RealmRemovalMailQueued   RealmRemovalMailState = "queued"
	RealmRemovalMailFailed   RealmRemovalMailState = "failed"
	RealmRemovalMailCanceled RealmRemovalMailState = "canceled"
)

// RealmRemovalMailJob contains the short-lived delivery secret. It never
// appears in the removal record nor in a control-plane response.
type RealmRemovalMailJob struct {
	Schema          string                `json:"schema"`
	MessageID       string                `json:"message_id"`
	OperationID     string                `json:"operation_id"`
	DeliveryAddress string                `json:"delivery_address,omitempty"`
	OTP             string                `json:"otp,omitempty"`
	DeliveryState   RealmRemovalMailState `json:"delivery_state"`
	AttemptID       string                `json:"attempt_id,omitempty"`
	AttemptedAt     *time.Time            `json:"attempted_at,omitempty"`
	QueuedAt        *time.Time            `json:"queued_at,omitempty"`
	LastError       string                `json:"last_error,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	ExpiresAt       time.Time             `json:"expires_at"`
}
type RealmRemovalRecord struct {
	Schema       string              `json:"schema"`
	OperationID  string              `json:"operation_id"`
	RealmID      string              `json:"realm_id"`
	Scope        RealmRemovalScope   `json:"scope"`
	Request      RealmRemovalRequest `json:"request"`
	OTPHash      string              `json:"otp_hash,omitempty"`
	AttemptsLeft int                 `json:"attempts_left"`
	State        RealmRemovalState   `json:"state"`
	CreatedAt    time.Time           `json:"created_at"`
	ConfirmedAt  *time.Time          `json:"confirmed_at,omitempty"`
	ExpiresAt    time.Time           `json:"expires_at"`
}

const realmRemovalSchema = "filees.realm-removal/v1"
const realmRemovalMailSchema = "filees.realm-removal-mail/v1"

func (s RealmRemovalStore) Begin(realmID string, scope RealmRemovalScope, request RealmRemovalRequest) (RealmRemovalRecord, string, error) {
	return s.BeginOperation(uuid.NewString(), realmID, scope, request)
}

// BeginOperation binds the immutable server-derived scope to the control
// operation ID. Retrying a request after the record was committed returns the
// same scope and never issues a second OTP.
func (s RealmRemovalStore) BeginOperation(operationID, realmID string, scope RealmRemovalScope, request RealmRemovalRequest) (RealmRemovalRecord, string, error) {
	if err := s.valid(); err != nil {
		return RealmRemovalRecord{}, "", err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return RealmRemovalRecord{}, "", errors.New("realm removal operation_id must be UUID")
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return RealmRemovalRecord{}, "", errors.New("realm removal realm_id must be UUID")
	}
	if err := validateScope(scope); err != nil {
		return RealmRemovalRecord{}, "", err
	}
	email, err := onboarding.CanonicalEmail(request.NotificationEmail)
	if err != nil {
		return RealmRemovalRecord{}, "", errors.New("realm removal notification email is invalid")
	}
	request.NotificationEmail = email
	now := s.now()
	token, err := newRealmRemovalOTP()
	if err != nil {
		return RealmRemovalRecord{}, "", err
	}
	record := RealmRemovalRecord{Schema: realmRemovalSchema, OperationID: operationID, RealmID: realmID, Scope: scope, Request: request, OTPHash: s.hash(token), AttemptsLeft: s.Attempts, State: RealmRemovalAwaitingConfirmation, CreatedAt: now, ExpiresAt: now.Add(s.TTL)}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return RealmRemovalRecord{}, "", err
	}
	result, resultToken := record, token
	err = WithFileLock(filepath.Join(s.Root, ".realm-removal.lock"), func() error {
		if existing, err := s.load(operationID); err == nil {
			if existing.RealmID != realmID || existing.Request != request {
				return errors.New("realm removal operation conflicts with prior request")
			}
			result, resultToken = existing, ""
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := atomicJSON(s.path(record.OperationID), record); err != nil {
			return err
		}
		if err := os.MkdirAll(s.outboxRoot(), 0700); err != nil {
			_ = os.Remove(s.path(record.OperationID))
			return err
		}
		job := RealmRemovalMailJob{Schema: realmRemovalMailSchema, MessageID: uuid.NewString(), OperationID: record.OperationID, DeliveryAddress: request.NotificationEmail, OTP: token, DeliveryState: RealmRemovalMailPending, CreatedAt: now, ExpiresAt: record.ExpiresAt}
		if err := atomicJSON(s.mailPath(record.OperationID), job); err != nil {
			_ = os.Remove(s.path(record.OperationID))
			return err
		}
		return nil
	})
	return result, resultToken, err
}
func (s RealmRemovalStore) Confirm(operationID, otp string) (RealmRemovalRecord, error) {
	if err := s.valid(); err != nil {
		return RealmRemovalRecord{}, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return RealmRemovalRecord{}, errors.New("realm removal operation_id must be UUID")
	}
	var out RealmRemovalRecord
	err := WithFileLock(filepath.Join(s.Root, ".realm-removal.lock"), func() error {
		raw, err := os.ReadFile(s.path(operationID))
		if err != nil {
			return err
		}
		var r RealmRemovalRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Schema != realmRemovalSchema || r.OperationID != operationID {
			return errors.New("realm removal record invalid")
		}
		if r.State != RealmRemovalAwaitingConfirmation {
			return errors.New("realm removal is not awaiting confirmation")
		}
		if !s.now().Before(r.ExpiresAt) {
			r.State = RealmRemovalExpired
			r.OTPHash = ""
			_ = atomicJSON(s.path(operationID), r)
			return errors.New("realm removal OTP expired")
		}
		if !hmac.Equal([]byte(r.OTPHash), []byte(s.hash(strings.TrimSpace(otp)))) {
			r.AttemptsLeft--
			if r.AttemptsLeft <= 0 {
				r.State = RealmRemovalExpired
				r.OTPHash = ""
			}
			_ = atomicJSON(s.path(operationID), r)
			return errors.New("realm removal OTP invalid")
		}
		r.OTPHash = ""
		r.AttemptsLeft = 0
		r.State = RealmRemovalDeleting
		confirmedAt := s.now()
		r.ConfirmedAt = &confirmedAt
		if err := atomicJSON(s.path(operationID), r); err != nil {
			return err
		}
		// The confirmed operation no longer needs a delivery secret. A sender
		// that already copied a claimed message may still finish SMTP, but can
		// no longer update this outbox record as if it were needed.
		if job, err := s.loadMail(s.mailPath(operationID)); err == nil && (job.DeliveryState == RealmRemovalMailPending || job.DeliveryState == RealmRemovalMailSending) {
			job.DeliveryState, job.DeliveryAddress, job.OTP, job.AttemptID, job.AttemptedAt = RealmRemovalMailCanceled, "", "", "", nil
			if err := atomicJSON(s.mailPath(operationID), job); err != nil {
				return err
			}
		}
		out = r
		return nil
	})
	return out, err
}
func (s RealmRemovalStore) Load(operationID string) (RealmRemovalRecord, error) {
	if err := s.valid(); err != nil {
		return RealmRemovalRecord{}, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return RealmRemovalRecord{}, errors.New("realm removal operation_id must be UUID")
	}
	raw, err := os.ReadFile(s.path(operationID))
	if err != nil {
		return RealmRemovalRecord{}, err
	}
	var r RealmRemovalRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return RealmRemovalRecord{}, err
	}
	if r.Schema != realmRemovalSchema || r.OperationID != operationID {
		return RealmRemovalRecord{}, errors.New("realm removal record invalid")
	}
	return r, nil
}

// PendingConfirmed enumerates only operations which crossed the OTP boundary
// and therefore must be finished server-side even if every client credential
// has already been revoked.
func (s RealmRemovalStore) PendingConfirmed() ([]RealmRemovalRecord, error) {
	if err := s.valid(); err != nil {
		return nil, err
	}
	var pending []RealmRemovalRecord
	err := WithFileLock(filepath.Join(s.Root, ".realm-removal.lock"), func() error {
		paths, err := filepath.Glob(filepath.Join(s.Root, "*.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var record RealmRemovalRecord
			if json.Unmarshal(raw, &record) != nil || record.Schema != realmRemovalSchema {
				return errors.New("realm removal record invalid")
			}
			switch record.State {
			case RealmRemovalDeleting, RealmRemovalRecoveryReady, RealmRemovalRevokingClients:
				pending = append(pending, record)
			}
		}
		sort.Slice(pending, func(i, j int) bool {
			return pending[i].OperationID < pending[j].OperationID
		})
		return nil
	})
	return pending, err
}

// ClaimPendingMail atomically reserves one OTP mail for submission. A stale
// sending claim is recovered, while a confirmed or expired operation causes
// its secret to be scrubbed rather than mailed late.
func (s RealmRemovalStore) ClaimPendingMail(staleAfter time.Duration) (RealmRemovalMailJob, error) {
	if err := s.mailValid(staleAfter); err != nil {
		return RealmRemovalMailJob{}, err
	}
	var claimed RealmRemovalMailJob
	err := WithFileLock(filepath.Join(s.Root, ".realm-removal.lock"), func() error {
		paths, err := filepath.Glob(filepath.Join(s.outboxRoot(), "*.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			job, err := s.loadMail(path)
			if err != nil {
				return err
			}
			record, err := s.load(job.OperationID)
			if err != nil {
				return err
			}
			now := s.now()
			if record.State != RealmRemovalAwaitingConfirmation || !now.Before(record.ExpiresAt) {
				if !now.Before(record.ExpiresAt) && record.State == RealmRemovalAwaitingConfirmation {
					record.State, record.OTPHash = RealmRemovalExpired, ""
					if err := atomicJSON(s.path(record.OperationID), record); err != nil {
						return err
					}
				}
				if job.DeliveryState == RealmRemovalMailPending || job.DeliveryState == RealmRemovalMailSending {
					job.DeliveryState, job.DeliveryAddress, job.OTP, job.AttemptID, job.AttemptedAt = RealmRemovalMailCanceled, "", "", "", nil
					if err := atomicJSON(path, job); err != nil {
						return err
					}
				}
				continue
			}
			if job.DeliveryState == RealmRemovalMailSending && job.AttemptedAt != nil && now.Sub(*job.AttemptedAt) >= staleAfter {
				job.DeliveryState, job.AttemptID, job.AttemptedAt = RealmRemovalMailPending, "", nil
			}
			if job.DeliveryState != RealmRemovalMailPending {
				continue
			}
			job.DeliveryState, job.AttemptID = RealmRemovalMailSending, uuid.NewString()
			job.AttemptedAt, job.LastError = &now, ""
			if err := atomicJSON(path, job); err != nil {
				return err
			}
			claimed = job
			return nil
		}
		return os.ErrNotExist
	})
	return claimed, err
}

func (s RealmRemovalStore) MarkMailQueued(operationID, attemptID string) error {
	return s.markMail(operationID, attemptID, "", false, true)
}

func (s RealmRemovalStore) MarkMailFailed(operationID, attemptID, reason string, permanent bool) error {
	return s.markMail(operationID, attemptID, reason, permanent, false)
}

func (s RealmRemovalStore) markMail(operationID, attemptID, reason string, permanent, queued bool) error {
	if err := s.mailValid(time.Second); err != nil {
		return err
	}
	if _, err := uuid.Parse(operationID); err != nil || attemptID == "" {
		return errors.New("realm removal mail identifiers are invalid")
	}
	return WithFileLock(filepath.Join(s.Root, ".realm-removal.lock"), func() error {
		job, err := s.loadMail(s.mailPath(operationID))
		if err != nil {
			return err
		}
		if job.DeliveryState == RealmRemovalMailQueued && queued {
			return nil
		}
		if job.DeliveryState != RealmRemovalMailSending || job.AttemptID != attemptID {
			return errors.New("realm removal mail attempt mismatch")
		}
		now := s.now()
		if queued {
			job.DeliveryState, job.DeliveryAddress, job.OTP = RealmRemovalMailQueued, "", ""
			job.AttemptID, job.AttemptedAt, job.LastError, job.QueuedAt = "", nil, "", &now
		} else {
			job.LastError = safeMailError(reason)
			job.AttemptID, job.AttemptedAt = "", nil
			if permanent {
				job.DeliveryState, job.DeliveryAddress, job.OTP = RealmRemovalMailFailed, "", ""
			} else {
				job.DeliveryState = RealmRemovalMailPending
			}
		}
		return atomicJSON(s.mailPath(operationID), job)
	})
}

// Advance permits only the server-owned irreversible sequence after OTP.
func (s RealmRemovalStore) Advance(operationID string, from, to RealmRemovalState) (RealmRemovalRecord, error) {
	if err := s.valid(); err != nil {
		return RealmRemovalRecord{}, err
	}
	if !validRealmRemovalTransition(from, to) {
		return RealmRemovalRecord{}, errors.New("invalid realm removal transition")
	}
	var out RealmRemovalRecord
	err := WithFileLock(filepath.Join(s.Root, ".realm-removal.lock"), func() error {
		r, err := s.Load(operationID)
		if err != nil {
			return err
		}
		if r.State == to {
			out = r
			return nil
		}
		if r.State != from {
			return errors.New("realm removal state conflicts with requested transition")
		}
		r.State = to
		if err := atomicJSON(s.path(operationID), r); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}
func validRealmRemovalTransition(from, to RealmRemovalState) bool {
	return (from == RealmRemovalDeleting && to == RealmRemovalRecoveryReady) || (from == RealmRemovalRecoveryReady && to == RealmRemovalRevokingClients) || (from == RealmRemovalRevokingClients && to == RealmRemovalCompleted)
}
func (s RealmRemovalStore) valid() error {
	if !filepath.IsAbs(s.Root) || len(s.OTPPepper) < 32 || s.TTL <= 0 || s.Attempts <= 0 {
		return errors.New("realm removal store is incomplete")
	}
	return nil
}
func (s RealmRemovalStore) mailValid(staleAfter time.Duration) error {
	if !filepath.IsAbs(s.Root) || staleAfter <= 0 {
		return errors.New("realm removal mail store is incomplete")
	}
	return nil
}
func (s RealmRemovalStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s RealmRemovalStore) path(id string) string { return filepath.Join(s.Root, id+".json") }
func (s RealmRemovalStore) outboxRoot() string    { return filepath.Join(s.Root, "outbox") }
func (s RealmRemovalStore) mailPath(id string) string {
	return filepath.Join(s.outboxRoot(), id+".json")
}
func (s RealmRemovalStore) load(operationID string) (RealmRemovalRecord, error) {
	raw, err := os.ReadFile(s.path(operationID))
	if err != nil {
		return RealmRemovalRecord{}, err
	}
	var record RealmRemovalRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Schema != realmRemovalSchema || record.OperationID != operationID {
		return RealmRemovalRecord{}, errors.New("realm removal record invalid")
	}
	return record, nil
}
func (s RealmRemovalStore) loadMail(path string) (RealmRemovalMailJob, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RealmRemovalMailJob{}, err
	}
	var job RealmRemovalMailJob
	if err := json.Unmarshal(raw, &job); err != nil || job.Schema != realmRemovalMailSchema {
		return RealmRemovalMailJob{}, errors.New("realm removal mail record invalid")
	}
	if _, err := uuid.Parse(job.OperationID); err != nil {
		return RealmRemovalMailJob{}, errors.New("realm removal mail record invalid")
	}
	if _, err := uuid.Parse(job.MessageID); err != nil {
		return RealmRemovalMailJob{}, errors.New("realm removal mail record invalid")
	}
	return job, nil
}
func (s RealmRemovalStore) hash(otp string) string {
	h := hmac.New(sha256.New, s.OTPPepper)
	h.Write([]byte(otp))
	return fmt.Sprintf("%x", h.Sum(nil))
}
func newRealmRemovalOTP() (string, error) {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}
func validateScope(scope RealmRemovalScope) error {
	for _, ids := range [][]string{scope.ClientIDs, scope.OwnedRepoIDs, scope.ForeignGrantRepoIDs} {
		seen := map[string]bool{}
		for _, id := range ids {
			if _, err := uuid.Parse(id); err != nil {
				return errors.New("realm removal scope ID must be UUID")
			}
			if seen[id] {
				return errors.New("realm removal scope contains duplicate ID")
			}
			seen[id] = true
		}
	}
	return nil
}

func safeMailError(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "\r", " "), "\n", " ")
	if len(value) > 240 {
		value = value[:240]
	}
	if value == "" {
		return "mail submission failed"
	}
	return value
}
