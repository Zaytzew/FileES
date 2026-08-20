//go:build !windows

package repoworker

import (
	"fmt"
	"os"
	"syscall"
)

func verifyServiceWorkingCopyOwner(path string) error {
	return verifyServiceWorkingCopyOwnerForUID(path, os.Geteuid())
}

func verifyServiceWorkingCopyOwnerForUID(path string, effectiveUID int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect service working-copy owner: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errorsNoServiceWorkingCopyOwnership(path)
	}
	if int(stat.Uid) != effectiveUID {
		return fmt.Errorf("service working copy %s is owned by uid %d, but effective uid is %d; run FileES administrative commands as the service-state owner", path, stat.Uid, effectiveUID)
	}
	return nil
}

func errorsNoServiceWorkingCopyOwnership(path string) error {
	return fmt.Errorf("service working copy %s exposes no ownership information", path)
}
