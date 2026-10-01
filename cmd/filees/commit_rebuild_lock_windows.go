//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

// Acquire the native byte-range lease before retiring the live .filees
// namespace. Close this child handle before renaming its enclosing WC.
func withCanonicalWriterLease(path string, run func(*os.File) error) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return os.ErrInvalid
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, opened) {
		return errors.New("native writer inode changed")
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		return err
	}
	defer windows.UnlockFileEx(h, 0, 1, 0, &overlapped)
	return run(f)
}

// Kernel lease, not a stale marker: process death releases it automatically.
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
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		return err
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
	return run()
}
