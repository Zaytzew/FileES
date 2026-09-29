package intake

import (
	"errors"
	"os"
)

// The public host is Unix. A Windows build keeps a fail-closed marker, like
// repoworker.WithFileLock, instead of pretending to hold a kernel lock.
func withBudgetLock(path string, fn func() error) error {
	lock := path + ".exclusive"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return errors.New("upload intake budget is busy")
	}
	f.Close()
	defer os.Remove(lock)
	return fn()
}
