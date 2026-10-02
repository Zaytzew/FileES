//go:build !windows

package repoworker

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"time"
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
	return WithFileLockContext(context.Background(), path, fn)
}

// WithFileLockContext bounds lock admission, not the operation's cleanup.
func WithFileLockContext(ctx context.Context, path string, fn func() error) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e == nil {
			break
		}
		if e != unix.EAGAIN && e != unix.EWOULDBLOCK && e != unix.EINTR {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

// FileLocksSupported reports whether this platform provides the kernel file
// locks the worker relies on. See lock_windows.go.
func FileLocksSupported() bool { return true }
