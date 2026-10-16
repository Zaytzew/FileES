//go:build !windows

package repoworker

import (
	"fmt"
	"os"
	"syscall"
)

func historyPhysicalIdentity(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ErrPathOwnerUnavailable
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
