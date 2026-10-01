//go:build !windows

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// APR may use POSIX record locking or flock. Hold both non-blocking leases on
// the stable inode so either native implementation is fenced through the swap.
func withCanonicalWriterLease(path string, run func(*os.File) error) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return os.ErrInvalid
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, opened) {
		return errors.New("native writer inode changed")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock); err != nil {
		return err
	}
	defer func() { lock.Type = unix.F_UNLCK; _ = unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock) }()
	return run(f)
}

func withCanonicalRecoveryLock(path string, run func() error) error {
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return os.ErrInvalid
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return run()
}
