package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultRunLockPath = "/var/run/filees-install.lock"

type runLock struct {
	file *os.File
}

// acquireRunLock serializes every action that can observe or mutate installer
// state. The advisory lock is held by an open descriptor, so a crash or kill
// releases it without stale-lock cleanup. The file deliberately stays outside
// DataDir: --purge --wipe-state must not unlink a live lock and let a second
// updater enter concurrently.
func (r *Runner) acquireRunLock() (*runLock, error) {
	path := strings.TrimSpace(r.Config.LockPath)
	if path == "" {
		// Runners assembled directly by tests and embedding callers may predate
		// Config.LockPath. Keep those isolated by their configured state path.
		if strings.TrimSpace(r.Config.StateDir) != "" {
			path = r.Config.StateDir + ".lock"
		} else {
			path = defaultRunLockPath
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("installer lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open installer lock %s: %w", path, err)
	}
	if err := lockRunFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another filees-install action is already running (lock %s): %w", path, err)
	}
	return &runLock{file: file}, nil
}

func (l *runLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
