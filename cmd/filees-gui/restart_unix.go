//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// restartCurrentGUI replaces the current GUI image in place.  A child process
// would be killed together with its parent when FileES runs in a systemd user
// unit, leaving the user with no tray entry after confirming a restart.
func restartCurrentGUI(argv []string) error {
	if len(argv) == 0 {
		return errors.New("missing GUI process arguments")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	return syscall.Exec(executable, append([]string{executable}, argv[1:]...), os.Environ())
}
