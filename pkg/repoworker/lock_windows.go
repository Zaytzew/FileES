package repoworker

import (
	"errors"
	"os"
)

func WithFileLock(path string, fn func() error) error {
	lock := path + ".exclusive"
	f, e := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return errors.New("repository worker is already active")
	}
	f.Close()
	defer os.Remove(lock)
	return fn()
}

// Scheduled server maintenance is supported only on Unix. Keep the Windows
// build fail-closed rather than treating a stale marker as a kernel lock.
func TryWithFileLock(string, func() error) error {
	return errors.New("scheduled passport maintenance requires Unix file locks")
}

// FileLocksSupported reports whether this platform provides the kernel file
// locks the worker relies on. Windows keeps a marker file instead and is
// deliberately fail-closed (see WithFileLock above), so a caller's tests can
// ask this rather than naming platforms themselves.
func FileLocksSupported() bool { return false }
