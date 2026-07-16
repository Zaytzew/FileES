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
	if err := obsandbox.Begin(entryPromises); err != nil {
		return err
	}
	profile := obsandbox.Profile{Name: "filees-entry/exec-worker", Promises: entryPromises, Paths: []obsandbox.Path{
		{Label: "worker", Name: workerPath, Perms: "rx"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
	}}
	if err := obsandbox.ApplyForExec(profile, workerPromises+" proc unveil"); err != nil {
		return err
	}
	return syscall.Exec(workerPath, []string{"filees-worker", "deploy"}, []string{})
}
