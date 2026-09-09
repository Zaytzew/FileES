//go:build !windows

package repoworker

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// TryWithFileLock never queues a periodic worker behind an active request.
func TryWithFileLock(path string, fn func() error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrFileLockBusy
		}
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}

func WithFileLock(path string, fn func() error) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX); e != nil {
		return e
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}

// FileLocksSupported reports whether this platform provides the kernel file
// locks the worker relies on. See lock_windows.go.
func FileLocksSupported() bool { return true }
