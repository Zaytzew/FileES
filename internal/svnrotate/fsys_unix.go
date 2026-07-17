//go:build unix

package svnrotate

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// acquireLock takes an exclusive non-blocking flock on path, guarding
// against concurrent rotator runs. The returned release function unlocks
// and closes; the lock file itself is left in place.
func acquireLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another rotation appears to be running (flock %s): %w", path, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

// sameFilesystem reports whether both paths live on the same device, which
// os.Rename between them requires (EXDEV otherwise — the exact failure mode
// the V1 prototype had with its /tmp work dir).
func sameFilesystem(a, b string) (bool, error) {
	var sa, sb unix.Stat_t
	if err := unix.Stat(a, &sa); err != nil {
		return false, fmt.Errorf("stat %s: %w", a, err)
	}
	if err := unix.Stat(b, &sb); err != nil {
		return false, fmt.Errorf("stat %s: %w", b, err)
	}
	return uint64(sa.Dev) == uint64(sb.Dev), nil
}
