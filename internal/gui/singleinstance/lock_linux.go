//go:build linux

package singleinstance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type fileLock struct {
	file *os.File
}

// Acquire obtains a non-blocking per-user advisory lock.
func Acquire(id string) (Lock, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		var err error
		dir, err = os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("single-instance directory: %w", err)
		}
		dir = filepath.Join(dir, "filees")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("single-instance directory: %w", err)
	}
	path := filepath.Join(dir, id+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("single-instance lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("single-instance lock: %w", err)
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
