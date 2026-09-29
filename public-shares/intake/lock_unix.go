//go:build !windows

package intake

import (
	"os"

	"golang.org/x/sys/unix"
)

// withBudgetLock serialises admission across public-host processes. It is a
// local copy of repoworker.WithFileLock: intake cannot import repoworker
// (repoworker → uploadworker → intake).
func withBudgetLock(path string, fn func() error) error {
	// Intake and reaper use different users in the shared _filees-public group.
	// Only the creator chmods; another user's existing inode must stay shared.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, jobFilePerm)
	if err == nil {
		if err := f.Chmod(jobFilePerm); err != nil {
			f.Close()
			return err
		}
	} else if os.IsExist(err) {
		f, err = os.OpenFile(path, os.O_RDWR, 0)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}
