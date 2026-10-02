//go:build linux || openbsd

package storagewatch

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func privatePolicyMode(mode os.FileMode) bool { return mode.Perm()&0077 == 0 }

// An administrator may run the monitor as root. Atomic replacement must not
// transfer ownership away from the existing state service/repository owner.
func preservePolicyOwner(f *os.File, target string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	var st unix.Stat_t
	if err := unix.Lstat(target, &st); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := unix.Stat(filepath.Dir(target), &st); err != nil {
			return err
		}
	}
	return f.Chown(int(st.Uid), int(st.Gid))
}
