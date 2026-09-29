//go:build unix

package repoworker

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// sameVolume reports whether both paths live on one filesystem, so a load's
// spool and its new generation compete for the same free space.
func sameVolume(a, b string) (bool, error) {
	var sa, sb unix.Stat_t
	if err := unix.Stat(a, &sa); err != nil {
		return false, fmt.Errorf("stat %s: %w", a, err)
	}
	if err := unix.Stat(b, &sb); err != nil {
		return false, fmt.Errorf("stat %s: %w", b, err)
	}
	return uint64(sa.Dev) == uint64(sb.Dev), nil
}
