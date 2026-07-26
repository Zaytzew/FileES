//go:build !windows

package servertool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestSessionSupervisorTerminatesChildOnRevoke(t *testing.T) {
	if runtime.GOOS == "openbsd" {
		t.Skip("native supervisor behavior is covered by the OpenBSD audit-lab acceptance test")
	}
	manager, config, grant, lease := newSupervisorTestSession(t)
	t.Cleanup(func() { _ = lease.Close() })
	originalStarter := startSessionChild
	t.Cleanup(func() { startSessionChild = originalStarter })
	startSessionChild = sessionSupervisorTestChild

	input, keepInputOpen := io.Pipe()
	defer keepInputOpen.Close()
	done := make(chan error, 1)
	go func() {
		done <- runSVNSessionSupervisor(config, grant.ClientID, manager, lease, input, io.Discard, io.Discard)
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := manager.Revoke(context.Background(), grant.ClientID, "supervisor FIFO test"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor returned error after revoke: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("supervisor did not end after revoke")
	}
}

func TestSessionSupervisorRelaysOpaqueBytes(t *testing.T) {
	if runtime.GOOS == "openbsd" {
		t.Skip("native supervisor behavior is covered by the OpenBSD audit-lab acceptance test")
	}
	manager, config, grant, lease := newSupervisorTestSession(t)
	t.Cleanup(func() { _ = lease.Close() })
	originalStarter := startSessionChild
	t.Cleanup(func() { startSessionChild = originalStarter })
	startSessionChild = sessionRelayTestChild
	const payload = "svn-protocol-bytes\x00stay-opaque"
	var stdout bytes.Buffer
	if err := runSVNSessionSupervisor(config, grant.ClientID, manager, lease, strings.NewReader(payload), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != payload {
		t.Fatalf("relay changed bytes: got %q want %q", got, payload)
	}
}

func TestSessionSupervisorProfileAllowsLeaseCleanup(t *testing.T) {
	config := serverconfig.Config{Activation: activation.Config{Root: "/var/filees/activation"}}
	lease := &activation.SessionLease{Dir: "/var/filees/activation/sessions/session-0123456789abcdef0123456789abcdef"}
	profile := sessionSupervisorProfile(config, lease)
	paths := map[string]string{}
	for _, path := range profile.Paths {
		paths[path.Label] = path.Perms
	}
	if paths["session-root-cleanup"] != "c" {
		t.Fatalf("session root cleanup permissions = %q, want c", paths["session-root-cleanup"])
	}
	if paths["session-lease"] != "rwc" {
		t.Fatalf("session lease permissions = %q, want rwc", paths["session-lease"])
	}
}

func sessionSupervisorTestChild(_ serverconfig.Config, _ string, _ string, _ string, gate, stdin, stdout, stderr *os.File) (*exec.Cmd, error) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		return nil, err
	}
	command := exec.Command("/bin/sleep", "600")
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	command.ExtraFiles = []*os.File{gate}
	command.Env = []string{}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command, nil
}

func sessionRelayTestChild(_ serverconfig.Config, _ string, _ string, _ string, gate, stdin, stdout, stderr *os.File) (*exec.Cmd, error) {
	if _, err := os.Stat("/bin/cat"); err != nil {
		return nil, err
	}
	command := exec.Command("/bin/cat")
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	command.ExtraFiles = []*os.File{gate}
	command.Env = []string{}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command, nil
}

func newSupervisorTestSession(t *testing.T) (*activation.Manager, serverconfig.Config, onboarding.ActivationGrant, *activation.SessionLease) {
	t.Helper()
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	svnserve, err := exec.LookPath("svnserve")
	if err != nil {
		t.Skip("svnserve unavailable")
	}
	root := t.TempDir()
	repository, wc := filepath.Join(root, "repository"), filepath.Join(root, "wc")
	runSupervisorCommand(t, svnadmin, "create", repository)
	runSupervisorCommand(t, svn, "mkdir", "--non-interactive", "--no-auth-cache", "-m", "init proof", "file://"+repository+"/proof")
	runSupervisorCommand(t, svn, "checkout", "--non-interactive", "--no-auth-cache", "file://"+repository, wc)
	activationConfig := activation.Config{
		Root: filepath.Join(root, "activation"), SessionRoot: filepath.Join(root, "sessions"),
		AuthorizedKeysFile: filepath.Join(root, "authorized_keys"), AuthzFile: filepath.Join(root, "authz"),
		ServiceWorkingCopy: wc, ServiceRepository: repository, RepositoryName: "filees-service",
		ClientEntryPath: os.Args[0], SVNBinary: svn, SVNServeBinary: svnserve,
	}
	manager, err := activation.New(activationConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	grant := onboarding.ActivationGrant{
		OperationID: uuid.NewString(), DeployRequestID: uuid.NewString(), ClientID: uuid.NewString(), RealmID: uuid.NewString(),
		InstallationPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " filees:test",
		InstallationFingerprint: ssh.FingerprintSHA256(key), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	lease, err := manager.ClaimSession(grant.OperationID, grant.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	return manager, serverconfig.Config{Activation: activationConfig}, grant, lease
}

func runSupervisorCommand(t *testing.T, command string, args ...string) {
	t.Helper()
	if output, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", command, args, err, output)
	}
}
