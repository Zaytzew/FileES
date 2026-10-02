//go:build !windows

package intake

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"time"
)

// WithReceiveBarrier uses the existing shared intake directory, never the
// private activation root. A stop is published by the authority first; the
// monitor's exclusive lease acknowledges completion of already admitted HTTP
// receives. Neither this lock nor its inode is removed by ordinary cleanup.
func WithReceiveBarrier(ctx context.Context, root string, exclusive bool, run func() error) error {
	if !filepath.IsAbs(root) || run == nil {
		return ErrIncomplete
	}
	path := filepath.Join(root, ".capacity-receivers.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(jobFilePerm))
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "capacity-receivers")
	defer f.Close()
	if created {
		if os.Geteuid() == 0 {
			var st unix.Stat_t
			if err := unix.Stat(root, &st); err != nil {
				return err
			}
			if err := f.Chown(int(st.Uid), int(st.Gid)); err != nil {
				return err
			}
		}
		if err := f.Chmod(jobFilePerm); err != nil {
			return err
		}
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != jobFilePerm {
		return ErrBudgetState
	}
	op := unix.LOCK_SH
	if exclusive {
		op = unix.LOCK_EX
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = unix.Flock(fd, op|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN && err != unix.EINTR {
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
