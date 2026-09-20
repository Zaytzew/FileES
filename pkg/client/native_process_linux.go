//go:build linux

package client

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// The helper and the OpenSSH tunnel belong to this command, not the daemon's
// process group. Cancellation must release both before another attempt.
func configureNativeProcess(command *exec.Cmd) func() {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Cancel = kill
	return func() { _ = kill() }
}
