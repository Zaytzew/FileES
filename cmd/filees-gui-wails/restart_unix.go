//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// restartCurrentProcess replaces the drained Wails process in place. Spawning
// and releasing a child is not sufficient for a GUI launched from a terminal:
// the child remains in the old PTY session and may receive SIGHUP when that
// session closes. Exec also mirrors the daemon's proven Unix restart path.
func restartCurrentProcess(argv []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	args := []string{executable}
	if len(argv) > 1 {
		args = append(args, argv[1:]...)
	}
	return syscall.Exec(executable, args, os.Environ())
}
