package servertool

import (
	"fmt"
	"io"
	"syscall"

	"filees/internal/obsandbox"
)

const (
	TunnelCommand = "filees tunnel-v1"
	// MobileOnboardCommand is the mobile-pairing analogue of TunnelCommand:
	// the phone presents its pairing token exactly where a desktop client
	// presents its mailed OTP (same _filees-tunnel BSD-Auth class, same
	// AuthenticateOTP), then pushes its installation public key over this
	// forced command instead of negotiating a reverse tunnel - mobile never
	// needs one (see concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md SS4.2).
	MobileOnboardCommand = "filees mobile-onboard-v1"
	workerPath           = "/usr/local/libexec/filees/filees-worker"
	entryPromises        = "stdio proc exec"
)

func RunEntry(args []string, _ io.Reader, _ io.Writer, stderr io.Writer, getenv func(string) string) int {
	return runEntry(args, stderr, getenv, execWorker, execMobileOnboardWorker)
}

func runEntry(args []string, stderr io.Writer, getenv func(string) string, executeTunnel, executeMobileOnboard func() error) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "filees-entry: rejected command")
		return ExitUnavailable
	}
	var execute func() error
	switch getenv("SSH_ORIGINAL_COMMAND") {
	case TunnelCommand:
		execute = executeTunnel
	case MobileOnboardCommand:
		execute = executeMobileOnboard
	default:
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
	//
	// "deploy" eventually opens its files with needSVN, which re-pledges with
	// execpromises=svnExecPromises (includes prot_exec, for the native SVN
	// binary's own W^X setup) ahead of an actual exec into svn. execpromises
	// can only ever narrow across a pledge chain, never widen beyond what was
	// granted here at exec time - so prot_exec must already be present in this
	// grant even though filees-worker itself never calls prot_exec directly.
	if err := obsandbox.PledgeForExec(entryPromises, workerPromises+" prot_exec unveil"); err != nil {
		return err
	}
	return syscall.Exec(workerPath, []string{"filees-worker", "deploy"}, []string{})
}

func execMobileOnboardWorker() error {
	// Same boundary discipline as execWorker: this dispatcher never touches
	// onboarding/activation state itself, it only hands off to the
	// already-audited filees-worker binary, which installs its own narrower
	// unveil before reading anything.
	if err := obsandbox.PledgeForExec(entryPromises, workerPromises+" unveil"); err != nil {
		return err
	}
	return syscall.Exec(workerPath, []string{"filees-worker", "mobile-onboard"}, []string{})
}
