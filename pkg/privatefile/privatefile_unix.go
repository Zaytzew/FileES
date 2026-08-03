//go:build !windows

package privatefile

import (
	"fmt"
	"os"
	"syscall"
)

// EnsureDir creates path and every missing parent, then forces the result to
// owner-only. The explicit Chmod is not redundant: MkdirAll applies the
// process umask, and it does nothing at all when the directory already
// exists, so an inherited world-readable directory would otherwise survive.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

// Harden restricts an existing file or directory to its owner.
func Harden(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info.IsDir() {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

// Verify reports ErrNotPrivate unless path is owned by the current user and
// carries no group or other access. It deliberately checks a mask rather than
// an exact mode: 0600 and 0700 differ only by the execute bit a directory
// needs, and neither says anything more than "nobody else may reach this".
func Verify(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%w: %s has mode %#o", ErrNotPrivate, path, perm)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %s exposes no ownership information", ErrNotPrivate, path)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is owned by uid %d", ErrNotPrivate, path, stat.Uid)
	}
	return nil
}
