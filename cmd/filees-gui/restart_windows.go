//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// Windows cannot replace a running executable in place, so it starts a
// detached successor after the current instance has released its lock.
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
	command := exec.Command(executable, argv[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
