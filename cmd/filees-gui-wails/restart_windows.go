//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// The replacement is told whom it is replacing. Windows has no exec that
	// swaps the process image, so this process is still alive when the next one
	// starts, and the single-instance lock would turn the restart into a silent
	// shutdown.
	command.Env = append(os.Environ(), handoverEnv+"="+strconv.Itoa(os.Getpid()))
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
