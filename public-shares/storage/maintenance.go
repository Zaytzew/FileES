package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filees/internal/durable"
)

const DefaultCleanupInterval = 5 * time.Minute
const maintenanceStatus = ".maintenance-status.json"

func CleanupInterval(value string) (time.Duration, error) {
	if value == "" {
		return DefaultCleanupInterval, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < time.Second || d > time.Hour {
		return 0, errors.New("cleanup_interval must be between 1s and 1h")
	}
	return d, nil
}

// Owner is held for the entire service lifetime, including active transfers.
// The lock file is permanent: unlinking it would permit two owners.
type Owner struct {
	root *os.Root
	file *os.File
}

func Own(root string) (*Owner, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return nil, errors.New("maintenance requires an absolute dedicated root")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("maintenance root must be a private real directory")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	const name = ".maintenance.lock"
	if info, err := r.Lstat(name); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		r.Close()
		return nil, errors.New("unsafe maintenance lock")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		r.Close()
		return nil, err
	}
	f, err := r.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		r.Close()
		return nil, err
	}
	closeFailed := func(err error) (*Owner, error) { f.Close(); r.Close(); return nil, err }
	opened, err := f.Stat()
	if err != nil {
		return closeFailed(err)
	}
	current, err := r.Lstat(name)
	if err != nil {
		return closeFailed(err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return closeFailed(errors.New("maintenance lock changed"))
	}
	if err := lockOwner(f); err != nil {
		return closeFailed(fmt.Errorf("storage already owned or lock unavailable: %w", err))
	}
	return &Owner{root: r, file: f}, nil
}

func (o *Owner) Close() error { return errors.Join(o.file.Close(), o.root.Close()) }

type SweepResult struct {
	Entries int64 `json:"removed_entries"`
	Files   int64 `json:"removed_files"`
	Bytes   int64 `json:"removed_bytes"`
	Active  int64 `json:"active_skipped"`
}

func (r *SweepResult) Add(other SweepResult) {
	r.Entries += other.Entries
	r.Files += other.Files
	r.Bytes += other.Bytes
	r.Active += other.Active
}

// Go CreateTemp uses a non-empty decimal suffix. Unknown files are retained.
func TempName(name, prefix string) bool {
	if len(name) <= len(prefix)+len(".tmp") || name[:len(prefix)] != prefix || filepath.Ext(name) != ".tmp" {
		return false
	}
	for _, c := range name[len(prefix) : len(name)-4] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// RemoveRegular never removes directories or follows a final symlink. Root
// confines every operation even if a parent path is unexpectedly replaced.
func RemoveRegular(root *os.Root, name string) (SweepResult, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return SweepResult{}, nil
	}
	if err != nil {
		return SweepResult{}, err
	}
	if !info.Mode().IsRegular() {
		return SweepResult{}, fmt.Errorf("refuse non-regular maintenance file %q", name)
	}
	if err := root.Remove(name); err != nil {
		return SweepResult{}, err
	}
	return SweepResult{Files: 1, Bytes: info.Size()}, nil
}

type MaintenanceStatus struct {
	Schema      string    `json:"schema"`
	State       string    `json:"state"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	SweepResult
	Error string `json:"error,omitempty"`
}

// Maintenance shares the service's store/tracker, never a second Store with
// an independent mutex. Passes do not overlap and continue after a failure.
type Maintenance struct {
	Root        string
	Interval    time.Duration
	Sweep       func(context.Context, time.Time) (SweepResult, error)
	Report      func(error)
	mu          sync.Mutex
	lastSuccess time.Time
}

func (m *Maintenance) Pass(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := MaintenanceStatus{Schema: "filees.public-storage-maintenance/v1", State: "running", StartedAt: now.UTC(), LastSuccess: m.lastSuccess}
	if err := m.save(status); err != nil {
		return err
	}
	budget := m.Interval
	if budget <= 0 || budget > time.Minute {
		budget = time.Minute
	}
	passCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	result, err := m.Sweep(passCtx, now)
	if passCtx.Err() != nil {
		err = errors.Join(err, passCtx.Err())
	}
	tmpResult, tmpErr := m.sweepStatusTemps(passCtx)
	result.Add(tmpResult)
	err = errors.Join(err, tmpErr)
	status.SweepResult = result
	status.FinishedAt = time.Now().UTC()
	status.State = "checked"
	if err != nil {
		status.State = "failed"
		status.Error = err.Error()
		if len(status.Error) > 2048 {
			status.Error = status.Error[:2048]
		}
	} else {
		status.LastSuccess = status.FinishedAt
	}
	if saveErr := m.save(status); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	if err == nil {
		m.lastSuccess = status.LastSuccess
	}
	return err
}

func (m *Maintenance) sweepStatusTemps(ctx context.Context) (SweepResult, error) {
	var result SweepResult
	r, err := os.OpenRoot(m.Root)
	if err != nil {
		return result, err
	}
	defer r.Close()
	dir, err := r.Open(".")
	if err != nil {
		return result, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return result, err
	}
	var failures error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(failures, err)
		}
		if !TempName(entry.Name(), ".maintenance-status-") {
			continue
		}
		removed, e := RemoveRegular(r, entry.Name())
		result.Add(removed)
		failures = errors.Join(failures, e)
	}
	return result, failures
}

// Run performs a startup pass then timed passes without traffic. Cancellation
// ends maintenance; callers retain Owner until all request handlers finish.
func (m *Maintenance) Run(ctx context.Context) {
	interval := m.Interval
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	run := func() {
		if err := m.Pass(ctx, time.Now()); err != nil && m.Report != nil {
			m.Report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			err := m.save(MaintenanceStatus{Schema: "filees.public-storage-maintenance/v1", State: "stopped", LastSuccess: m.lastSuccess})
			m.mu.Unlock()
			if err != nil && m.Report != nil {
				m.Report(err)
			}
			return
		case <-ticker.C:
			run()
		}
	}
}

func (m *Maintenance) Start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	return func() { cancel(); <-done }
}

// Activity closes admission before Wait, preventing a late Add/Wait race.
type Activity struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	stopped bool
}

func (a *Activity) Enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return false
	}
	a.wg.Add(1)
	return true
}
func (a *Activity) Leave()       { a.wg.Done() }
func (a *Activity) StopAndWait() { a.mu.Lock(); a.stopped = true; a.mu.Unlock(); a.wg.Wait() }

func (m *Maintenance) save(status MaintenanceStatus) error {
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.Root, ".maintenance-status-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(m.Root, maintenanceStatus)); err != nil {
		return err
	}
	return durable.SyncDirectory(m.Root)
}

func CheckMaintenance(root string, interval time.Duration, now time.Time) (MaintenanceStatus, error) {
	var status MaintenanceStatus
	r, err := os.OpenRoot(root)
	if err != nil {
		return status, err
	}
	defer r.Close()
	info, err := r.Lstat(maintenanceStatus)
	if err != nil {
		return status, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<10 {
		return status, errors.New("unsafe maintenance status")
	}
	f, err := r.Open(maintenanceStatus)
	if err != nil {
		return status, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 16<<10))
	if err := decoder.Decode(&status); err != nil {
		return status, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return status, errors.New("invalid maintenance status tail")
	}
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	if status.Schema != "filees.public-storage-maintenance/v1" || status.State != "checked" || status.StartedAt.IsZero() || status.LastSuccess.IsZero() || status.FinishedAt.Before(status.StartedAt) || !status.LastSuccess.Equal(status.FinishedAt) || now.Before(status.FinishedAt) || now.Sub(status.FinishedAt) > 2*interval || status.Files < 0 || status.Bytes < 0 || status.Active < 0 || status.Entries < 0 {
		return status, errors.New("storage maintenance failed, running or stale")
	}
	return status, nil
}
