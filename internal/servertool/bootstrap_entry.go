package servertool

import (
	"fmt"
	"io"
	"os/exec"

	"filees/internal/obsandbox"
)

const (
	bootstrapEntryPromises = "stdio rpath proc exec"
	bootstrapChildPromises = "stdio rpath wpath cpath fattr flock inet dns proc unveil"
	onboardPath            = "/usr/local/libexec/filees/filees-onboard"
	mailPath               = "/usr/local/libexec/filees/filees-mail"
)

// RunBootstrapEntry is the bounded forced-command transaction behind the
// public bootstrap SSH key. It consumes one ticket and immediately submits
// exactly one pending mail job; it never listens or remains resident.
func RunBootstrapEntry(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "filees-bootstrap-entry: arguments are not accepted")
		return ExitUsage
	}
	if err := bootstrapPledge(bootstrapEntryPromises, bootstrapChildPromises); err != nil {
		report(stderr, "filees-bootstrap-entry sandbox", err)
		return ExitSoftware
	}
	if err := runBootstrapChild(onboardPath, []string{"take"}, stdin, stdout, stderr); err != nil {
		report(stderr, "filees-bootstrap-entry onboard", err)
		return ExitTempFail
	}
	if err := runBootstrapChild(mailPath, []string{"send"}, nil, io.Discard, stderr); err != nil {
		report(stderr, "filees-bootstrap-entry mail", err)
		return ExitTempFail
	}
	return ExitOK
}

var bootstrapPledge = obsandbox.PledgeForExec

var runBootstrapChild = func(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.Command(path, args...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	return command.Run()
}
