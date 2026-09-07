package repoworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// PassportReapStatus is operational evidence, never an acquisition receipt.
// A stale running record means the process died or stalled, not success.
type PassportReapStatus struct {
	Schema     string    `json:"schema"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// ScheduledPassportReaper is a single cron pass. It reads no grants, requires
// no connected client, and never reconciles or publishes the service WC.
// The existing two locks still serialize repository lifecycle operations.
type ScheduledPassportReaper struct {
	Executions                          PassportExecutions
	WorkerLock, ServiceLock, StatusFile string
}

func (s ScheduledPassportReaper) Run(ctx context.Context) (PassportReapStatus, error) {
	status := PassportReapStatus{Schema: "filees.passport-maintenance/v1", State: "busy", StartedAt: s.Executions.now()}
	if err := ValidateMaintenanceRoot(s.Executions.Authority.Locks.RepositoriesRoot); err != nil {
		return status, err
	}
	for _, path := range []string{s.WorkerLock, s.ServiceLock, s.StatusFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
			return status, errors.New("absolute scoped maintenance paths required")
		}
	}
	err := TryWithFileLock(s.WorkerLock, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := privateExecutionFile(s.StatusFile); err != nil {
			return err
		}
		status.State = "running"
		if err := atomicJSON(s.StatusFile, status); err != nil {
			return err
		}
		err := TryWithFileLock(s.ServiceLock, func() error {
			return s.Executions.Reap(ctx)
		})
		status.FinishedAt = s.Executions.now()
		status.State = "checked"
		if err != nil {
			status.State, status.Error = "failed", err.Error()
			if len(status.Error) > 4096 {
				status.Error = status.Error[:4096]
			}
		}
		return errors.Join(err, atomicJSON(s.StatusFile, status))
	})
	if err != nil && status.Error == "" {
		status.Error = err.Error()
	}
	// Missing registry is a normal empty deployment. Missing configured roots
	// are not: callers must validate them before invoking this operation.
	return status, err
}

// CheckPassportMaintenance fails when cron stopped, a pass failed or died,
// or the server clock moved behind the recorded completion time.
func CheckPassportMaintenance(status PassportReapStatus, now time.Time, maxAge time.Duration) error {
	if status.Schema != "filees.passport-maintenance/v1" || status.State != "checked" || status.Error != "" ||
		status.StartedAt.IsZero() || status.FinishedAt.Before(status.StartedAt) || status.FinishedAt.IsZero() ||
		maxAge <= 0 || now.Before(status.FinishedAt) || now.Sub(status.FinishedAt) > maxAge {
		return errors.New("passport maintenance unhealthy or stale")
	}
	return nil
}

// ValidateMaintenanceRoot refuses missing or symlinked roots before cron can
// mistakenly report an empty successful scan of a missing configured tree.
func ValidateMaintenanceRoot(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return errors.New("maintenance root must be an absolute scoped directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("maintenance root must be a real directory")
	}
	return nil
}
