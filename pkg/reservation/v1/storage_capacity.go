package v1

import (
	"errors"
	"time"
)

// StorageCapacity describes the filesystem hosting the repository store, not
// a user quota or a grant to write. No device identities or paths are public.
// Zero lifetime preserves an expired observation; it never renews freshness.
type StorageCapacity struct {
	State           string     `json:"state"`
	TotalBytes      uint64     `json:"total_bytes,omitempty"`
	AvailableBytes  uint64     `json:"available_bytes,omitempty"`
	MeasuredAt      *time.Time `json:"measured_at,omitempty"`
	ValidForSeconds int        `json:"valid_for_seconds"`
}

func (s StorageCapacity) Validate() error {
	if s.State == "unknown" && s.TotalBytes == 0 && s.AvailableBytes == 0 && s.MeasuredAt == nil && s.ValidForSeconds == 0 {
		return nil
	}
	if s.State != "observed" || s.TotalBytes == 0 || s.TotalBytes > 9007199254740991 || s.AvailableBytes > s.TotalBytes || s.MeasuredAt == nil || s.MeasuredAt.IsZero() || s.ValidForSeconds < 0 || s.ValidForSeconds > 180 {
		return errors.New("invalid storage capacity observation")
	}
	return nil
}
