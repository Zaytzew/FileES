package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const DefaultReservationTTL = 24 * time.Hour

var ErrReservationUnavailable = errors.New("storage reservation is unavailable")

type ReservationLedger interface {
	Reserve(context.Context, string, int64, time.Time) (availableBytes, requiredBytes int64, expiresAt time.Time, err error)
	Ensure(context.Context, string, time.Time) error
	Release(string) error
}

type reservationRecord struct {
	OperationID   string `json:"operation_id"`
	ContentBytes  int64  `json:"content_bytes"`
	RequiredBytes int64  `json:"required_bytes"`
	ExpiresAt     string `json:"expires_at"`
}

type FileReservationLedger struct {
	Root     string
	Capacity CapacityChecker
	TTL      time.Duration
	mu       sync.Mutex
}

func (l *FileReservationLedger) Reserve(ctx context.Context, operationID string, contentBytes int64, now time.Time) (int64, int64, time.Time, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validate(); err != nil {
		return 0, 0, time.Time{}, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return 0, 0, time.Time{}, errors.New("reservation operation ID must be UUID")
	}
	available, required, err := l.Capacity.Check(ctx, contentBytes)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	records, err := l.liveLocked(now, operationID)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	effective := available
	for _, record := range records {
		if record.RequiredBytes >= effective {
			effective = 0
			break
		}
		effective -= record.RequiredBytes
	}
	if effective < required {
		return effective, required, time.Time{}, ErrReservationUnavailable
	}
	expires := now.UTC().Add(l.ttl())
	record := reservationRecord{OperationID: operationID, ContentBytes: contentBytes, RequiredBytes: required, ExpiresAt: expires.Format(time.RFC3339Nano)}
	if err := l.saveLocked(record); err != nil {
		return 0, 0, time.Time{}, err
	}
	return effective, required, expires, nil
}

// Ensure renews an expired reservation from its durable content estimate. This
// keeps a slow initial import resumable without trusting an obsolete capacity
// decision.
func (l *FileReservationLedger) Ensure(ctx context.Context, operationID string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validate(); err != nil {
		return err
	}
	record, err := l.loadLocked(operationID)
	if err != nil {
		return ErrReservationUnavailable
	}
	if _, err := time.Parse(time.RFC3339Nano, record.ExpiresAt); err != nil {
		return errors.New("invalid storage reservation expiry")
	}
	available, required, err := l.Capacity.Check(ctx, record.ContentBytes)
	if err != nil {
		return err
	}
	records, err := l.liveLocked(now, operationID)
	if err != nil {
		return err
	}
	for _, other := range records {
		if other.RequiredBytes >= available {
			return ErrReservationUnavailable
		}
		available -= other.RequiredBytes
	}
	if available < required {
		return ErrReservationUnavailable
	}
	record.RequiredBytes = required
	record.ExpiresAt = now.UTC().Add(l.ttl()).Format(time.RFC3339Nano)
	return l.saveLocked(record)
}

func (l *FileReservationLedger) Release(operationID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validate(); err != nil {
		return err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return errors.New("reservation operation ID must be UUID")
	}
	err := os.Remove(l.path(operationID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *FileReservationLedger) validate() error {
	if !filepath.IsAbs(l.Root) || l.Capacity == nil {
		return errors.New("storage reservation ledger is incomplete")
	}
	return os.MkdirAll(filepath.Clean(l.Root), 0700)
}

func (l *FileReservationLedger) ttl() time.Duration {
	if l.TTL > 0 {
		return l.TTL
	}
	return DefaultReservationTTL
}

func (l *FileReservationLedger) path(operationID string) string {
	return filepath.Join(filepath.Clean(l.Root), operationID+".json")
}

func (l *FileReservationLedger) loadLocked(operationID string) (reservationRecord, error) {
	raw, err := os.ReadFile(l.path(operationID))
	if err != nil {
		return reservationRecord{}, err
	}
	var record reservationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return reservationRecord{}, err
	}
	if record.OperationID != operationID || record.ContentBytes < 0 || record.RequiredBytes < 0 {
		return reservationRecord{}, errors.New("invalid storage reservation")
	}
	return record, nil
}

func (l *FileReservationLedger) liveLocked(now time.Time, exclude string) ([]reservationRecord, error) {
	entries, err := os.ReadDir(filepath.Clean(l.Root))
	if err != nil {
		return nil, err
	}
	var records []reservationRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		op := strings.TrimSuffix(entry.Name(), ".json")
		record, err := l.loadLocked(op)
		if err != nil {
			return nil, err
		}
		expires, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if !expires.After(now) {
			if err := os.Remove(l.path(op)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			continue
		}
		if op != exclude {
			records = append(records, record)
		}
	}
	return records, nil
}

func (l *FileReservationLedger) saveLocked(record reservationRecord) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Clean(l.Root), ".reservation-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, l.path(record.OperationID))
}
