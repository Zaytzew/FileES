//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package activation

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func withFileLock(path string, run func() error) error {
	return withFileLockContext(context.Background(), path, run)
}

func withFileLockContext(ctx context.Context, path string, run func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EAGAIN && err != unix.EWOULDBLOCK && err != unix.EINTR {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return run()
}
