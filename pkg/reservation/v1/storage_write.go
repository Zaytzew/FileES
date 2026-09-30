package v1

import (
	"errors"
	"time"
)

// StorageWrite describes only observed capacity for this result's repository.
// "available" grants no permission and reserves no bytes. ValidForSeconds is
// remaining lifetime at response construction; clients must subtract transit
// time, never renew it from a cache read, and must not treat absence as health.
type StorageWrite struct {
	State           string     `json:"state"`
	MeasuredAt      *time.Time `json:"measured_at,omitempty"`
	ValidForSeconds int        `json:"valid_for_seconds"`
}

func validateStorageWrite(r Result) error {
	s := r.StorageWrite
	if s == nil {
		return nil
	}
	if (r.Schema != StateSchema && r.Schema != AutolockSchema) || r.RepoID == "" || r.RepositoryState != "active" {
		return errors.New("storage write state outside active repository selector")
	}
	switch s.State {
	case "unknown":
		if s.MeasuredAt == nil && s.ValidForSeconds == 0 {
			return nil
		}
	case "available", "blocked":
		if s.MeasuredAt != nil && !s.MeasuredAt.IsZero() && s.ValidForSeconds > 0 && s.ValidForSeconds <= 180 {
			return nil
		}
	}
	return errors.New("invalid storage write observation")
}
