//go:build linux || openbsd

package mobileworker

import (
	"os"
	"os/exec"
)

// The SVN child retains the SAME flock open-file description if its parent
// worker dies. A restarted worker cannot conclude "no commit" until this
// child has exited too. Never call LOCK_UN on the parent's descriptor.
func inheritOperationLock(cmd *exec.Cmd, lock *os.File) error {
	cmd.ExtraFiles = append(cmd.ExtraFiles, lock)
	return nil
}
