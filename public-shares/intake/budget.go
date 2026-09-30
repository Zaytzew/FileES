package intake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
	"github.com/google/uuid"
)

var (
	ErrChannelFull    = errors.New("upload channel has reached its pending upload limit")
	ErrQuarantineFull = errors.New("upload quarantine has reached its size limit")
	ErrBudgetState    = errors.New("upload intake usage cannot be established")
)

const (
	reservationName   = "RESERVED"
	budgetLockName    = ".intake-budget.lock"
	maxBudgetMetadata = 16 << 10
)

type reservation struct {
	ChannelID string    `json:"channel_id"`
	Bytes     int64     `json:"bytes"`
	At        time.Time `json:"at"`
}

func (s Store) limited() bool { return s.MaxUploadsPerChannel > 0 || s.MaxQuarantineBytes > 0 }

// Admission and reservation are atomic across processes, before reading any
// payload. Reservations NEVER expire by age: abandoned bytes still occupy disk.
// Only removal of the job or publication of its measured size releases budget.
func (s Store) reserveLocked(channelID, uploadID string) error {
	pending, total, err := s.usage(channelID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBudgetState, err)
	}
	if s.MaxUploadsPerChannel > 0 && pending >= s.MaxUploadsPerChannel {
		return ErrChannelFull
	}
	if s.MaxQuarantineBytes > 0 && (total > s.MaxQuarantineBytes || s.MaxBytes > s.MaxQuarantineBytes-total) {
		return ErrQuarantineFull
	}
	dir := filepath.Join(s.Root, uploadID)
	if err := os.Mkdir(dir, jobDirPerm); err != nil {
		return err
	}
	raw, err := json.Marshal(reservation{ChannelID: channelID, Bytes: s.MaxBytes, At: s.now()})
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, reservationName), raw, jobFilePerm)
	}
	if err == nil {
		file, openErr := os.OpenFile(filepath.Join(dir, reservationName), os.O_RDWR, jobFilePerm)
		if openErr != nil {
			err = openErr
		} else {
			err = errors.Join(file.Sync(), file.Close())
		}
	}
	if err == nil {
		err = durable.SyncDirectory(dir)
	}
	if err == nil {
		err = durable.SyncDirectory(s.Root)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
	}
	return err
}

// Called under the same lock as admission, publication, Claim/Release and Remove.
// Quota covers logical payload bytes, not filesystem allocation or TrashRoot.
func (s Store) usage(channelID string) (pending int, total int64, err error) {
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return 0, 0, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return 0, 0, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if entry.Name() == budgetLockName || entry.Name() == budgetLockName+".exclusive" {
			continue
		}
		if !entry.IsDir() || !canonicalID(entry.Name()) {
			return 0, 0, ErrBudgetState
		}
		job, err := root.OpenRoot(entry.Name())
		if err != nil {
			return 0, 0, fmt.Errorf("%w: %v", ErrBudgetState, err)
		}
		owner, size, err := jobUsage(job, entry.Name())
		job.Close()
		if err != nil {
			return 0, 0, fmt.Errorf("%w: job %s: %v", ErrBudgetState, entry.Name(), err)
		}
		if size > math.MaxInt64-total {
			return 0, 0, ErrBudgetState
		}
		total += size
		if owner == channelID {
			pending++
		}
	}
	return pending, total, nil
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func readBudgetJSON(root *os.Root, name string, target any) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBudgetMetadata {
		return ErrBudgetState
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBudgetMetadata+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBudgetMetadata {
		return ErrBudgetState
	}
	return json.Unmarshal(raw, target)
}

func jobUsage(root *os.Root, uploadID string) (string, int64, error) {
	var held reservation
	err := readBudgetJSON(root, reservationName, &held)
	reserved := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}
	if reserved && (!canonicalID(held.ChannelID) || held.Bytes < 1 || held.At.IsZero()) {
		return "", 0, ErrBudgetState
	}
	// Inspect actual payloads as well: missing metadata is not free capacity.
	var actual int64
	for _, name := range []string{payloadName, "." + payloadName + ".tmp"} {
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", 0, err
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > math.MaxInt64-actual {
			return "", 0, ErrBudgetState
		}
		actual += info.Size()
	}
	if reserved {
		return held.ChannelID, max(held.Bytes, actual), nil
	}
	var record Record
	if err := readBudgetJSON(root, metaName, &record); err != nil {
		return "", 0, err
	}
	if record.Schema != Schema || record.UploadID != uploadID || !canonicalID(record.ChannelID) || record.State != StateReady || record.Size < 1 || actual != record.Size {
		return "", 0, ErrBudgetState
	}
	return record.ChannelID, actual, nil
}
