package repoworker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/onboarding"
	"github.com/google/uuid"
)

const (
	dataErasureSchema     = "filees.data-erasure-request/v1"
	dataErasureMailSchema = "filees.data-erasure-mail/v1"
)

type DataErasureState string

const (
	DataErasureRequested               DataErasureState = "requested"
	DataErasureAwaitingBackupRetention DataErasureState = "awaiting_backup_retention"
	DataErasureCompleted               DataErasureState = "completed"
	DataErasurePartiallyRetained       DataErasureState = "partially_retained"
)

// DataErasureRecord is deliberately separate from the realm-removal journal.
// It records a user's privacy request without claiming that server backups can
// be selectively purged before the operator has actually verified that fact.
type DataErasureRecord struct {
	Schema              string           `json:"schema"`
	OperationID         string           `json:"operation_id"`
	RealmID             string           `json:"realm_id"`
	NotificationEmail   string           `json:"notification_email,omitempty"`
	State               DataErasureState `json:"state"`
	RequestedAt         time.Time        `json:"requested_at"`
	ActiveDataDeletedAt *time.Time       `json:"active_data_deleted_at,omitempty"`
	CompletionDueAt     time.Time        `json:"completion_due_at"`
	CompletedAt         *time.Time       `json:"completed_at,omitempty"`
}

type DataErasureMailJob struct {
	Schema          string                `json:"schema"`
	MessageID       string                `json:"message_id"`
	OperationID     string                `json:"operation_id"`
	DeliveryAddress string                `json:"delivery_address,omitempty"`
	FinalState      DataErasureState      `json:"final_state"`
	DeliveryState   RealmRemovalMailState `json:"delivery_state"`
	AttemptID       string                `json:"attempt_id,omitempty"`
	AttemptedAt     *time.Time            `json:"attempted_at,omitempty"`
	QueuedAt        *time.Time            `json:"queued_at,omitempty"`
	LastError       string                `json:"last_error,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
}

type DataErasureStore struct {
	Root string
	Now  func() time.Time
}

func (s DataErasureStore) Accept(removal RealmRemovalRecord, maxDays int) (DataErasureRecord, error) {
	if err := s.valid(); err != nil {
		return DataErasureRecord{}, err
	}
	if !removal.Request.ErasureRequested || removal.ConfirmedAt == nil {
		return DataErasureRecord{}, errors.New("data erasure requires a confirmed user request")
	}
	if maxDays <= 0 || maxDays > 3650 {
		return DataErasureRecord{}, errors.New("data erasure completion window is invalid")
	}
	email, err := onboarding.CanonicalEmail(removal.Request.NotificationEmail)
	if err != nil {
		return DataErasureRecord{}, errors.New("data erasure notification email is invalid")
	}
	requestedAt := removal.ConfirmedAt.UTC()
	record := DataErasureRecord{
		Schema: dataErasureSchema, OperationID: removal.OperationID, RealmID: removal.RealmID,
		NotificationEmail: email, State: DataErasureRequested, RequestedAt: requestedAt,
		CompletionDueAt: requestedAt.AddDate(0, 0, maxDays),
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return DataErasureRecord{}, err
	}
	var out DataErasureRecord
	err = WithFileLock(filepath.Join(s.Root, ".data-erasure.lock"), func() error {
		if existing, err := s.load(removal.OperationID); err == nil {
			if existing.RealmID != record.RealmID || !existing.RequestedAt.Equal(record.RequestedAt) ||
				!existing.CompletionDueAt.Equal(record.CompletionDueAt) {
				return errors.New("data erasure request conflicts with existing record")
			}
			out = existing
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := atomicJSON(s.path(record.OperationID), record); err != nil {
			return err
		}
		out = record
		return nil
	})
	return out, err
}

// MarkActiveDataDeleted records the truthful boundary reached by realm
// removal. Remaining backup/log retention is an operator responsibility.
func (s DataErasureStore) MarkActiveDataDeleted(operationID string) (DataErasureRecord, error) {
	return s.update(operationID, func(record *DataErasureRecord) error {
		if record.State == DataErasureAwaitingBackupRetention ||
			record.State == DataErasureCompleted || record.State == DataErasurePartiallyRetained {
			return nil
		}
		if record.State != DataErasureRequested {
			return errors.New("data erasure request is not awaiting active-data deletion")
		}
		now := s.now()
		record.ActiveDataDeletedAt = &now
		record.State = DataErasureAwaitingBackupRetention
		return nil
	})
}

// Complete is an explicit operator assertion. It never runs from a timer:
// backup topology must be checked before choosing completed vs partial.
func (s DataErasureStore) Complete(operationID string, partiallyRetained bool) (DataErasureRecord, error) {
	if err := s.valid(); err != nil {
		return DataErasureRecord{}, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return DataErasureRecord{}, errors.New("data erasure operation_id must be UUID")
	}
	var out DataErasureRecord
	err := WithFileLock(filepath.Join(s.Root, ".data-erasure.lock"), func() error {
		record, err := s.load(operationID)
		if err != nil {
			return err
		}
		target := DataErasureCompleted
		if partiallyRetained {
			target = DataErasurePartiallyRetained
		}
		if record.State != target && record.State != DataErasureAwaitingBackupRetention {
			return errors.New("data erasure request is not awaiting backup retention")
		}
		if record.State == DataErasureAwaitingBackupRetention {
			now := s.now()
			record.State, record.CompletedAt = target, &now
			if err := atomicJSON(s.path(operationID), record); err != nil {
				return err
			}
		}
		out = record
		if _, err := os.Stat(s.mailPath(operationID)); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		job := DataErasureMailJob{
			Schema:          dataErasureMailSchema,
			MessageID:       uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":data-erasure-completion")).String(),
			OperationID:     record.OperationID,
			DeliveryAddress: record.NotificationEmail, FinalState: target,
			DeliveryState: RealmRemovalMailPending, CreatedAt: *record.CompletedAt,
		}
		if err := os.MkdirAll(s.outboxRoot(), 0700); err != nil {
			return err
		}
		return atomicJSON(s.mailPath(record.OperationID), job)
	})
	return out, err
}

func (s DataErasureStore) ClaimPendingMail(staleAfter time.Duration) (DataErasureMailJob, error) {
	if err := s.valid(); err != nil || staleAfter <= 0 {
		return DataErasureMailJob{}, errors.New("data erasure mail store is incomplete")
	}
	var claimed DataErasureMailJob
	err := WithFileLock(filepath.Join(s.Root, ".data-erasure.lock"), func() error {
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
			expectedMessageID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(job.OperationID+":data-erasure-completion")).String()
			if record.State != job.FinalState || record.CompletedAt == nil || job.MessageID != expectedMessageID {
				return errors.New("data erasure mail is not bound to a completed request")
			}
			if (job.DeliveryState == RealmRemovalMailPending || job.DeliveryState == RealmRemovalMailSending) &&
				(record.NotificationEmail == "" || job.DeliveryAddress != record.NotificationEmail) {
				return errors.New("data erasure mail recipient does not match request")
			}
			now := s.now()
			if job.DeliveryState == RealmRemovalMailSending && job.AttemptedAt != nil && now.Sub(*job.AttemptedAt) >= staleAfter {
				job.DeliveryState, job.AttemptID, job.AttemptedAt = RealmRemovalMailPending, "", nil
			}
			if job.DeliveryState != RealmRemovalMailPending {
				continue
			}
			job.DeliveryState, job.AttemptID, job.AttemptedAt = RealmRemovalMailSending, uuid.NewString(), &now
			job.LastError = ""
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

func (s DataErasureStore) MarkMailQueued(operationID, attemptID string) error {
	return s.markMail(operationID, attemptID, "", false, true)
}

func (s DataErasureStore) MarkMailFailed(operationID, attemptID, reason string, permanent bool) error {
	return s.markMail(operationID, attemptID, reason, permanent, false)
}

func (s DataErasureStore) markMail(operationID, attemptID, reason string, permanent, queued bool) error {
	if err := s.valid(); err != nil || attemptID == "" {
		return errors.New("data erasure mail identifiers are invalid")
	}
	return WithFileLock(filepath.Join(s.Root, ".data-erasure.lock"), func() error {
		job, err := s.loadMail(s.mailPath(operationID))
		if err != nil {
			return err
		}
		if job.DeliveryState == RealmRemovalMailQueued && queued {
			return s.scrubNotification(operationID)
		}
		if job.DeliveryState != RealmRemovalMailSending || job.AttemptID != attemptID {
			return errors.New("data erasure mail attempt mismatch")
		}
		now := s.now()
		job.AttemptID, job.AttemptedAt = "", nil
		if queued {
			job.DeliveryState, job.DeliveryAddress, job.LastError, job.QueuedAt = RealmRemovalMailQueued, "", "", &now
		} else if permanent {
			job.DeliveryState, job.DeliveryAddress, job.LastError = RealmRemovalMailFailed, "", safeMailError(reason)
		} else {
			job.DeliveryState, job.LastError = RealmRemovalMailPending, safeMailError(reason)
		}
		if queued || permanent {
			if err := s.scrubNotification(operationID); err != nil {
				return err
			}
		}
		return atomicJSON(s.mailPath(operationID), job)
	})
}

func (s DataErasureStore) Load(operationID string) (DataErasureRecord, error) {
	if err := s.valid(); err != nil {
		return DataErasureRecord{}, err
	}
	return s.load(operationID)
}

func (s DataErasureStore) update(operationID string, mutate func(*DataErasureRecord) error) (DataErasureRecord, error) {
	if err := s.valid(); err != nil {
		return DataErasureRecord{}, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return DataErasureRecord{}, errors.New("data erasure operation_id must be UUID")
	}
	var out DataErasureRecord
	err := WithFileLock(filepath.Join(s.Root, ".data-erasure.lock"), func() error {
		record, err := s.load(operationID)
		if err != nil {
			return err
		}
		if err := mutate(&record); err != nil {
			return err
		}
		if err := atomicJSON(s.path(operationID), record); err != nil {
			return err
		}
		out = record
		return nil
	})
	return out, err
}

func (s DataErasureStore) valid() error {
	if !filepath.IsAbs(s.Root) {
		return errors.New("data erasure store root must be absolute")
	}
	return nil
}

func (s DataErasureStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s DataErasureStore) path(operationID string) string {
	return filepath.Join(s.Root, operationID+".json")
}
func (s DataErasureStore) outboxRoot() string { return filepath.Join(s.Root, "outbox") }
func (s DataErasureStore) mailPath(operationID string) string {
	return filepath.Join(s.outboxRoot(), operationID+".json")
}

func (s DataErasureStore) load(operationID string) (DataErasureRecord, error) {
	raw, err := os.ReadFile(s.path(operationID))
	if err != nil {
		return DataErasureRecord{}, err
	}
	var record DataErasureRecord
	if json.Unmarshal(raw, &record) != nil || record.Schema != dataErasureSchema ||
		record.OperationID != operationID || record.RealmID == "" ||
		(record.NotificationEmail == "" && record.State != DataErasureCompleted && record.State != DataErasurePartiallyRetained) {
		return DataErasureRecord{}, errors.New("data erasure record is invalid")
	}
	if _, err := uuid.Parse(record.OperationID); err != nil {
		return DataErasureRecord{}, errors.New("data erasure record is invalid")
	}
	if _, err := uuid.Parse(record.RealmID); err != nil {
		return DataErasureRecord{}, errors.New("data erasure record is invalid")
	}
	switch record.State {
	case DataErasureRequested, DataErasureAwaitingBackupRetention:
		if record.CompletedAt != nil {
			return DataErasureRecord{}, errors.New("data erasure record is invalid")
		}
	case DataErasureCompleted, DataErasurePartiallyRetained:
		if record.CompletedAt == nil {
			return DataErasureRecord{}, errors.New("data erasure record is invalid")
		}
	default:
		return DataErasureRecord{}, errors.New("data erasure record is invalid")
	}
	if record.RequestedAt.IsZero() || !record.CompletionDueAt.After(record.RequestedAt) {
		return DataErasureRecord{}, errors.New("data erasure record is invalid")
	}
	return record, nil
}

// scrubNotification is called with .data-erasure.lock held.
func (s DataErasureStore) scrubNotification(operationID string) error {
	record, err := s.load(operationID)
	if err != nil {
		return err
	}
	if record.NotificationEmail == "" {
		return nil
	}
	record.NotificationEmail = ""
	return atomicJSON(s.path(operationID), record)
}

func (s DataErasureStore) loadMail(path string) (DataErasureMailJob, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return DataErasureMailJob{}, err
	}
	var job DataErasureMailJob
	if json.Unmarshal(raw, &job) != nil || job.Schema != dataErasureMailSchema ||
		job.OperationID == "" || job.MessageID == "" {
		return DataErasureMailJob{}, errors.New("data erasure mail record is invalid")
	}
	return job, nil
}
