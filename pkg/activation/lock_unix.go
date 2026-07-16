//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package activation

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func withFileLock(path string, run func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return run()
}
