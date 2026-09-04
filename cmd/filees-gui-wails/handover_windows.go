//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
)

// handoverTimeout bounds the wait for the replaced instance.
//
// Bounded rather than indefinite: if the process being replaced hangs on the
// way out, the right outcome is an interface that starts anyway - a second tray
// icon is a visible nuisance, a missing one is an invisible outage.
const handoverTimeout = 15 * time.Second

// awaitReplacedInstance blocks until the process named by handoverEnv is gone.
func awaitReplacedInstance() error {
	raw, present := os.LookupEnv(handoverEnv)
	if !present || raw == "" {
		return nil
	}
	// Cleared before waiting: this process may itself be restarted later, and a
	// stale pid inherited by that restart would make it wait on a stranger.
	os.Unsetenv(handoverEnv)
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return fmt.Errorf("%s=%q is not a process id", handoverEnv, raw)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// Already gone, or not ours to wait on. Either way there is nothing to
		// wait for, and starting is the useful thing to do.
		return nil
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, uint32(handoverTimeout/time.Millisecond))
	if err != nil {
		return fmt.Errorf("waiting for the replaced client (pid %d): %w", pid, err)
	}
	if state == uint32(windows.WAIT_TIMEOUT) {
		return errors.New("the replaced client (pid " + raw + ") is still running after " + handoverTimeout.String())
	}
	return nil
}
