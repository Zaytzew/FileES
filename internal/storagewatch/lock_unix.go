//go:build linux || openbsd

package storagewatch

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func Lock(path string) (*os.File, error) {
	fd, e := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "capacity-monitor")
	if e = preservePolicyOwner(f, path); e != nil {
		f.Close()
		return nil, e
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || !privatePolicyMode(info.Mode()) {
		f.Close()
		return nil, errors.New("unsafe capacity monitor lock")
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
