package repoworker

import "os/exec"

// Repository workers are deployed on Unix. Windows runs the portable tests;
// CommandContext retains its default direct-child cancellation here.
func configureDeletionProcess(command *exec.Cmd) {}
