//go:build linux || openbsd || darwin || freebsd || netbsd

package nativeruntime

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
)

func platformLease(path string, exclusive bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	mode := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		mode = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), mode); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errLeaseBusy
		}
		return nil, err
	}
	return f, nil
}
func inheritLease(cmd *exec.Cmd, f *os.File) error {
	cmd.ExtraFiles = append(cmd.ExtraFiles, f)
	return nil
}
