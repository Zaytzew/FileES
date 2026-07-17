package servertool

import (
	"fmt"
	"io"
	"syscall"

	"filees/internal/obsandbox"
)

const (
	TunnelCommand = "filees tunnel-v1"
	workerPath    = "/usr/local/libexec/filees/filees-worker"
	entryPromises = "stdio proc exec"
)

func RunEntry(args []string, _ io.Reader, _ io.Writer, stderr io.Writer, getenv func(string) string) int {
	return runEntry(args, stderr, getenv, execWorker)
}

func runEntry(args []string, stderr io.Writer, getenv func(string) string, execute func() error) int {
	if len(args) != 0 || getenv("SSH_ORIGINAL_COMMAND") != TunnelCommand {
		fmt.Fprintln(stderr, "filees-entry: rejected command")
		return ExitUnavailable
	}
	if err := execute(); err != nil {
		report(stderr, "filees-entry exec", err)
		return ExitSoftware
	}
	return ExitOK
}

func execWorker() error {
	// The command and argv are constants. A dispatcher unveil would survive
	// exec and prevent the worker from installing its own configuration-derived
	// exact profile, so this boundary uses pledge+execpromises only. The worker
	// immediately begins and locks its own unveil before opening state.
	if err := obsandbox.PledgeForExec(entryPromises, workerPromises+" unveil"); err != nil {
		return err
	}
	return syscall.Exec(workerPath, []string{"filees-worker", "deploy"}, []string{})
}
