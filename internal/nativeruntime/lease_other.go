//go:build !linux && !openbsd && !darwin && !freebsd && !netbsd && !windows

package nativeruntime

import (
	"errors"
	"os"
	"os/exec"
)

func platformLease(string, bool) (*os.File, error) {
	return nil, errors.New("runtime lifetime locks unsupported")
}
func inheritLease(*exec.Cmd, *os.File) error { return errors.New("runtime lifetime locks unsupported") }
