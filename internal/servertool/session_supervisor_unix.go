//go:build !windows

package servertool

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/activation"
	"filees/pkg/serverconfig"

	"github.com/google/uuid"
)

const (
	sessionPollInterval = time.Second
	sessionFIFOPoll     = 50 * time.Millisecond
	sessionKillGrace    = 2 * time.Second
)

// RunClientSessionChild is an internal, one-shot process mode. Its only
// authority is the inherited gate descriptor; it has no SSH command parsing
// and receives no caller-controlled argv from the forced-command path.
func RunClientSessionChild(args []string, stderr io.Writer) int {
	if len(args) != 4 {
		return ExitUsage
	}
	svnserve, root, clientID, nonce := args[0], args[1], args[2], args[3]
	if !filepath.IsAbs(svnserve) || !filepath.IsAbs(root) || strings.ContainsAny(svnserve+root, "\r\n") {
		return ExitUsage
	}
	if _, err := uuid.Parse(clientID); err != nil {
		return ExitUsage
	}
	if len(nonce) != 64 {
		return ExitUsage
	}
	decoded, err := hex.DecodeString(nonce)
	if err != nil || len(decoded) != 32 {
		return ExitUsage
	}
	gate := os.NewFile(uintptr(3), "filees-session-gate")
	if gate == nil {
		return ExitSoftware
	}
	defer gate.Close()
	expected := []byte(nonce)
	provided := make([]byte, len(expected))
	if _, err := io.ReadFull(gate, provided); err != nil || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return ExitUnavailable
	}
	if err := syscall.Setpgid(0, 0); err != nil {
		report(stderr, "filees-client-entry child process group", err)
		return ExitSoftware
	}
	// The child is forked before its parent locks unveil. svnserve therefore
	// gets a clean table and establishes its existing native profile itself.
	if err := sandboxPledgeForExec("stdio proc exec", svnExecPromises); err != nil {
		report(stderr, "filees-client-entry child sandbox", err)
		return ExitSoftware
	}
	if err := syscall.Exec(svnserve, []string{filepath.Base(svnserve), "-t", "--tunnel-user", clientID, "-r", root}, []string{}); err != nil {
		report(stderr, "filees-client-entry child exec", err)
		return ExitSoftware
	}
	return ExitSoftware
}

func runSVNSessionSupervisor(config serverconfig.Config, clientID string, manager *activation.Manager, lease *activation.SessionLease, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if lease == nil || manager == nil {
		return 0, errors.New("session supervisor requires an activation lease")
	}
	root := config.Activation.ServiceRepository
	if os.Getenv("USER") == "_filees-data" {
		root = config.Repositories.Root
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(config.Activation.ClientEntryPath) {
		return 0, errors.New("session supervisor received a non-absolute trusted path")
	}
	nonce, err := sessionNonce()
	if err != nil {
		return 0, err
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return 0, fmt.Errorf("create session gate: %w", err)
	}
	defer gateWrite.Close()
	childInRead, childInWrite, err := pipePair("child stdin")
	if err != nil {
		gateRead.Close()
		return 0, err
	}
	childOutRead, childOutWrite, err := pipePair("child stdout")
	if err != nil {
		gateRead.Close()
		childInRead.Close()
		childInWrite.Close()
		return 0, err
	}
	childErrRead, childErrWrite, err := pipePair("child stderr")
	if err != nil {
		gateRead.Close()
		childInRead.Close()
		childInWrite.Close()
		childOutRead.Close()
		childOutWrite.Close()
		return 0, err
	}

	cmd, err := startSessionChild(config, clientID, root, nonce, gateRead, childInRead, childOutWrite, childErrWrite)
	if err != nil {
		closeSessionPipes(gateRead, gateWrite, childInRead, childInWrite, childOutRead, childOutWrite, childErrRead, childErrWrite)
		return 0, fmt.Errorf("start session child: %w", err)
	}
	// The child now owns only private pipe ends and its gate. It never owns an
	// SSH descriptor. Close the parent's duplicate child ends before relay.
	gateRead.Close()
	childInRead.Close()
	childOutWrite.Close()
	childErrWrite.Close()

	if err := sandboxApply(sessionSupervisorProfile(config, lease)); err != nil {
		_ = terminateSessionChild(cmd.Process.Pid, nil)
		_ = cmd.Wait()
		closeSessionPipes(gateWrite, childInWrite, childOutRead, childErrRead)
		return 0, err
	}
	if !manager.SessionAllowed(lease.Metadata.OperationID, clientID) {
		_ = terminateSessionChild(cmd.Process.Pid, nil)
		_ = cmd.Wait()
		closeSessionPipes(gateWrite, childInWrite, childOutRead, childErrRead)
		return ExitUnavailable, nil
	}
	if _, err := io.WriteString(gateWrite, nonce); err != nil {
		_ = terminateSessionChild(cmd.Process.Pid, nil)
		_ = cmd.Wait()
		closeSessionPipes(gateWrite, childInWrite, childOutRead, childErrRead)
		return 0, fmt.Errorf("open session child gate: %w", err)
	}
	if err := gateWrite.Close(); err != nil {
		_ = terminateSessionChild(cmd.Process.Pid, nil)
		_ = cmd.Wait()
		closeSessionPipes(childInWrite, childOutRead, childErrRead)
		return 0, fmt.Errorf("close session child gate: %w", err)
	}

	return relaySession(cmd, manager, lease, stdin, stdout, stderr, childInWrite, childOutRead, childErrRead)
}

var startSessionChild = func(config serverconfig.Config, clientID, root, nonce string, gate, stdin, stdout, stderr *os.File) (*exec.Cmd, error) {
	cmd := exec.Command(config.Activation.ClientEntryPath, "--session-child", config.Activation.SVNServeBinary, root, clientID, nonce)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.ExtraFiles = []*os.File{gate}
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func sessionSupervisorProfile(config serverconfig.Config, lease *activation.SessionLease) obsandbox.Profile {
	sessionRoot := config.Activation.SessionRoot
	if sessionRoot == "" {
		sessionRoot = filepath.Join(config.Activation.Root, "sessions")
	}
	return obsandbox.Profile{Name: "filees-client-entry/session-supervisor", Promises: writePromises + " proc", Paths: []obsandbox.Path{
		{Label: "activation-records", Name: filepath.Join(config.Activation.Root, "records"), Perms: "r"},
		// The parent grants only create/remove authority needed to rmdir the
		// known lease. Cleanup removes two exact, validated artifacts and never
		// enumerates the session root.
		{Label: "session-root-cleanup", Name: sessionRoot, Perms: "c"},
		{Label: "session-lease", Name: lease.Dir, Perms: "rwc"},
	}}
}

func relaySession(cmd *exec.Cmd, manager *activation.Manager, lease *activation.SessionLease, stdin io.Reader, stdout, stderr io.Writer, childIn *os.File, childOut, childErr *os.File) (int, error) {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	var outputs sync.WaitGroup
	outputs.Add(2)
	go func() {
		defer outputs.Done()
		_, _ = io.Copy(stdout, childOut)
		_ = childOut.Close()
	}()
	go func() {
		defer outputs.Done()
		_, _ = io.Copy(stderr, childErr)
		_ = childErr.Close()
	}()
	go func() {
		_, _ = io.Copy(childIn, stdin)
		_ = childIn.Close()
	}()

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	poll := time.NewTicker(sessionPollInterval)
	defer poll.Stop()
	fifoPoll := time.NewTicker(sessionFIFOPoll)
	defer fifoPoll.Stop()
	for {
		select {
		case waitErr := <-wait:
			closeInput(stdin, childIn)
			outputs.Wait()
			return sessionChildExitCode(waitErr)
		case <-fifoPoll.C:
			revoked, err := lease.Revoked()
			if err != nil {
				closeInput(stdin, childIn)
				_ = terminateSessionChild(cmd.Process.Pid, wait)
				outputs.Wait()
				return 0, fmt.Errorf("poll session revoke lease: %w", err)
			}
			if revoked {
				closeInput(stdin, childIn)
				waitErr := terminateSessionChild(cmd.Process.Pid, wait)
				outputs.Wait()
				return sessionChildExitCode(waitErr)
			}
		case <-poll.C:
			if !manager.SessionAllowed(lease.Metadata.OperationID, lease.Metadata.ClientID) {
				closeInput(stdin, childIn)
				waitErr := terminateSessionChild(cmd.Process.Pid, wait)
				outputs.Wait()
				return sessionChildExitCode(waitErr)
			}
		}
	}
}

func terminateSessionChild(pid int, wait <-chan error) error {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if wait == nil {
		return nil
	}
	select {
	case waitErr := <-wait:
		return waitErr
	case <-time.After(sessionKillGrace):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return <-wait
	}
}

func sessionChildExitCode(waitErr error) (int, error) {
	if waitErr == nil {
		return ExitOK, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return 0, fmt.Errorf("wait for session child: %w", waitErr)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return 0, fmt.Errorf("read session child exit status: %w", waitErr)
	}
	if status.Exited() {
		return status.ExitStatus(), nil
	}
	if status.Signaled() {
		// Match the conventional shell representation while preserving the
		// distinction from a supervisor infrastructure failure.
		return 128 + int(status.Signal()), nil
	}
	return 0, fmt.Errorf("session child ended without an exit status: %w", waitErr)
}

func closeInput(stdin io.Reader, childIn *os.File) {
	if closer, ok := stdin.(io.Closer); ok {
		_ = closer.Close()
	}
	_ = childIn.Close()
}

func pipePair(label string) (*os.File, *os.File, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create %s pipe: %w", label, err)
	}
	return read, write, nil
}

func closeSessionPipes(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func sessionNonce() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session gate nonce: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
