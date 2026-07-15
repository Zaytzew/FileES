//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package onboarding

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (s *Files) withLock(action func() error) error {
	file, err := os.OpenFile(filepath.Join(s.root, lockName), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return action()
}
