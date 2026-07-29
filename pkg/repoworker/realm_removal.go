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
	"strings"
	"time"

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
type RealmRemovalRecord struct {
	Schema       string            `json:"schema"`
	OperationID  string            `json:"operation_id"`
	RealmID      string            `json:"realm_id"`
	Scope        RealmRemovalScope `json:"scope"`
	OTPHash      string            `json:"otp_hash,omitempty"`
	AttemptsLeft int               `json:"attempts_left"`
	State        RealmRemovalState `json:"state"`
	CreatedAt    time.Time         `json:"created_at"`
	ExpiresAt    time.Time         `json:"expires_at"`
}

const realmRemovalSchema = "filees.realm-removal/v1"

func (s RealmRemovalStore) Begin(realmID string, scope RealmRemovalScope) (RealmRemovalRecord, string, error) {
	if err := s.valid(); err != nil {
		return RealmRemovalRecord{}, "", err
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return RealmRemovalRecord{}, "", errors.New("realm removal realm_id must be UUID")
	}
	if err := validateScope(scope); err != nil {
		return RealmRemovalRecord{}, "", err
	}
	now := s.now()
	token, err := newRealmRemovalOTP()
	if err != nil {
		return RealmRemovalRecord{}, "", err
	}
	record := RealmRemovalRecord{Schema: realmRemovalSchema, OperationID: uuid.NewString(), RealmID: realmID, Scope: scope, OTPHash: s.hash(token), AttemptsLeft: s.Attempts, State: RealmRemovalAwaitingConfirmation, CreatedAt: now, ExpiresAt: now.Add(s.TTL)}
	err = WithFileLock(filepath.Join(s.Root, ".realm-removal.lock"), func() error {
		if err := os.MkdirAll(s.Root, 0700); err != nil {
			return err
		}
		return atomicJSON(s.path(record.OperationID), record)
	})
	return record, token, err
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
		if err := atomicJSON(s.path(operationID), r); err != nil {
			return err
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
func (s RealmRemovalStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s RealmRemovalStore) path(id string) string { return filepath.Join(s.Root, id+".json") }
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
