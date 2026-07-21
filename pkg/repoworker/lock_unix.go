//go:build !windows

package repoworker

import (
	"golang.org/x/sys/unix"
	"os"
)

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
