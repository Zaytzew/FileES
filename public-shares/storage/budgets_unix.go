//go:build linux || openbsd

package storage

import (
	"fmt"
	"os"
	"syscall"
)

func probeCapacity(path string) (string, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, fmt.Errorf("filesystem identity unavailable for %s", path)
	}
	available, err := Available(path)
	return fmt.Sprintf("dev:%d", stat.Dev), available, err
}
