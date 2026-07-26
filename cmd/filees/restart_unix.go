//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// restartCurrentProcess replaces the fully drained daemon in-place. Keeping
// the PID avoids a race with systemd --user and also works for a daemon
// started manually.
func restartCurrentProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	return syscall.Exec(executable, append([]string{executable}, os.Args[1:]...), os.Environ())
}
