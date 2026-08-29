//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func restartCurrentProcess(argv []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	args := []string(nil)
	if len(argv) > 1 {
		args = append(args, argv[1:]...)
	}
	command := exec.Command(executable, args...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
