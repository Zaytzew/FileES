//go:build !windows

package storage

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockOwner(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
