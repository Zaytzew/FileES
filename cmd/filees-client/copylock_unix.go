//go:build linux || openbsd || darwin || freebsd || netbsd

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func platformLockCopy(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
