//go:build !windows

package repoworker

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureDeletionProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		// freeze and its dump child share this private group. No PID scanning
		// or signalling unrelated workers. SIGKILL avoids an ignored TERM
		// leaving a writer alive while staging is being removed.
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
