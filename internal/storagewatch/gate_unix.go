//go:build linux || openbsd

package storagewatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func (g Gate) withLock(ctx context.Context, exclusive bool, run func() error) error {
	if !filepath.IsAbs(g.Root) {
		return errors.New("write gate root must be absolute")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(g.Root, ".capacity-writers.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "capacity-writers")
	defer f.Close()
	// Always derive this lock's owner from the private root, including the
	// first run by root. Its inode remains stable across policy replacements.
	if err := preservePolicyOwner(f, g.Root); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe capacity writer lock")
	}
	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(fd, operation|unix.LOCK_NB)
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
	defer unix.Flock(fd, unix.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return run()
}
