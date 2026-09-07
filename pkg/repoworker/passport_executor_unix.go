//go:build !windows

package repoworker

import "syscall"

// Only ESRCH is positive proof. PID reuse and all other outcomes are busy;
// no signal is sent and an unrelated process can never be killed.
func passportExecutorDead(pid int) (bool, error) {
	if pid <= 1 {
		return false, nil
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH, nil
}
