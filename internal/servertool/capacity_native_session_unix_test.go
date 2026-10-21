//go:build linux || openbsd

package servertool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"filees/internal/storagewatch"
	"filees/pkg/activation"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type capacitySessionFixture struct {
	Config      serverconfig.Config
	OperationID string
	ClientID    string
}

// Each native sandbox is installed in a subprocess: unveil/pledge must not
// constrain the test runner or be replaced by a no-op during acceptance.
func TestCapacityNativeSessionProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--session-child" {
			os.Exit(RunClientSessionChild(os.Args[i+1:], os.Stderr))
		}
	}
	path := os.Getenv("FILEES_TEST_CAPACITY_SESSION")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture capacitySessionFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	manager, err := activation.New(fixture.Config.Activation, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.ClaimSession(fixture.OperationID, fixture.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	code, err := runSessionSupervisor(fixture.Config, fixture.ClientID, fixture.Config.Activation.ServiceRepository, manager, lease, os.Stdin, os.Stdout, os.Stderr, startSessionChild)
	closeErr := lease.Close()
	if err != nil || closeErr != nil {
		fmt.Fprintln(os.Stderr, "fixture supervisor failed", err, closeErr)
		os.Exit(99)
	}
	os.Exit(code)
}

// svn may terminate its local tunnel command after closing the protocol.
// An SSH server's remote supervisor is a separate process group. Preserve
// that boundary here so killing the local transport does not kill the remote
// supervisor before it can acknowledge its child's exit.
func TestCapacityNativeTunnelProcess(t *testing.T) {
	if os.Getenv("FILEES_TEST_CAPACITY_SESSION") == "" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCapacityNativeSessionProcess$")
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	if err == nil {
		os.Exit(0)
	}
	if exit, ok := err.(*exec.ExitError); ok {
		os.Exit(exit.ExitCode())
	}
	os.Exit(99)
}

func TestCapacityNativeCommitHookProcess(t *testing.T) {
	if len(os.Args) != 5 || filepath.Base(os.Args[2]) != "pre-commit" {
		return
	}
	repo := os.Args[3]
	if !filepath.IsAbs(repo) || filepath.Dir(os.Args[2]) != filepath.Join(repo, "hooks") {
		os.Exit(99)
	}
	if err := os.WriteFile(filepath.Join(repo, "db", "commit-ready"), []byte("ready"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(99)
	}
	f, err := os.Open(filepath.Join(repo, "db", "commit-block"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(99)
	}
	defer f.Close()
	_, _ = io.Copy(io.Discard, f)
	os.Exit(0)
}

func TestCapacityNativeSVNReadWriteAndSessionDrain(t *testing.T) {
	manager, config, grant, initialLease := newSupervisorTestSession(t)
	if err := initialLease.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(config.Activation.Root)
	entry := filepath.Join(root, "client-entry")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nexec "+quote(os.Args[0])+" -test.run=^TestCapacityNativeSessionProcess$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config.Activation.ClientEntryPath = entry
	fixturePath := filepath.Join(root, "session-fixture.json")
	raw, err := json.Marshal(capacitySessionFixture{config, grant.OperationID, grant.ClientID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// A private test repository with authenticated write access. Realm ACL
	// coverage belongs to the separate authorization tests.
	if err := os.WriteFile(filepath.Join(config.Activation.ServiceRepository, "conf", "svnserve.conf"), []byte("[general]\nanon-access = none\nauth-access = write\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	env := append(os.Environ(), "FILEES_TEST_CAPACITY_SESSION="+fixturePath,
		"SVN_SSH="+quote(os.Args[0])+" -test.run=^TestCapacityNativeTunnelProcess$ --")
	call := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, config.Activation.SVNBinary, append([]string{"--non-interactive", "--no-auth-cache", "--config-dir", filepath.Join(root, "svn-config")}, args...)...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	url := "svn+ssh://capacity-test/"
	for i, held := range []bool{false, true, false} {
		gate := storagewatch.Gate{Root: config.Activation.Root}
		if held {
			if err := gate.Hold(); err != nil {
				t.Fatal(err)
			}
		} else if err := gate.WithExclusive(ctx, gate.Resume); err != nil {
			t.Fatal(err)
		}
		if out, err := call("list", url); err != nil || !strings.Contains(string(out), "proof/") {
			t.Fatalf("read during held=%v: %v %s", held, err, out)
		}
		out, err := call("mkdir", "-m", "capacity acceptance", url+fmt.Sprintf("attempt-%d", i))
		if held && (err == nil || !strings.Contains(string(out), "E170001")) {
			t.Fatalf("held session did not deny native SVN write: %v %s", err, out)
		}
		if !held && err != nil {
			t.Fatalf("open session did not allow native SVN write: %v %s", err, out)
		}
	}
	// An interrupted archive transition has the same raw-read semantics but
	// its own durable fence; it must not depend on a capacity monitor setting.
	archiveFence := repoworker.ArchiveAdmissionFence{Root: config.Activation.Root}
	archiveRepo := uuid.NewString()
	archivePlan := repoworker.RepositoryArchivePolicy{Schema: repoworker.RepositoryArchivePolicySchema, OperationID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Revision: 1, Epoch: 1, State: "preparing", StartedAt: time.Now().UTC()}
	if err := archiveFence.Hold(ctx, archiveRepo, archivePlan); err != nil {
		t.Fatal(err)
	}
	if out, err := call("list", url); err != nil || !strings.Contains(string(out), "proof/") {
		t.Fatalf("archive fence blocked reads: %v %s", err, out)
	}
	if out, err := call("mkdir", "-m", "forbidden", url+"archive-pending"); err == nil || !strings.Contains(string(out), "E170001") {
		t.Fatalf("archive fence allowed write: %v %s", err, out)
	}
	if err := archiveFence.Open(ctx, archiveRepo, archivePlan); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityNativeSessionProcess$")
	cmd.Env = env
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Real svnserve's protocol greeting, with its stdin deliberately kept open.
	if _, err := io.ReadFull(output, make([]byte, 1)); err != nil {
		_ = cmd.Wait()
		t.Fatal("no native SVN greeting", err, stderr.String())
	}
	gate := storagewatch.Gate{Root: config.Activation.Root}
	if err := gate.Hold(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SignalAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.WithAllSessionsStopped(ctx, func() error {
		return gate.WithExclusive(ctx, gate.AcknowledgeHeld)
	}); err != nil {
		_ = cmd.Process.Signal(syscall.SIGQUIT)
		_ = cmd.Wait()
		t.Fatal(err, stderr.String())
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("capacity signal did not terminate native SVN", stderr.String())
	}
	if !strings.Contains(stderr.String(), "FILEES-SESSION-ENDED") {
		t.Fatal("missing termination diagnostic", stderr.String())
	}
	if !manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("capacity stop revoked the activation")
	}
	if phase, err := gate.Phase(); phase != "held" || err != nil {
		t.Fatal(phase, err)
	}
	if out, err := call("list", url); err != nil {
		t.Fatalf("new read session after acknowledged HOLD: %v %s", err, out)
	}
	// Now stop an authenticated SVN session inside a real commit. Its hook
	// blocks before publication, so a completed barrier must leave HEAD intact.
	if err := gate.WithExclusive(ctx, gate.Resume); err != nil {
		t.Fatal(err)
	}
	repo := config.Activation.ServiceRepository
	if err := unix.Mkfifo(filepath.Join(repo, "db", "commit-block"), 0600); err != nil {
		t.Fatal(err)
	}
	// Native Go hook, like the deployed FileES hooks: do not give a shell
	// broader promises just to run a laboratory blocking command.
	hook := "#!" + os.Args[0] + " -test.run=^TestCapacityNativeCommitHookProcess$\n"
	if err := os.WriteFile(filepath.Join(repo, "hooks", "pre-commit"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	look, err := exec.LookPath("svnlook")
	if err != nil {
		t.Fatal(err)
	}
	before, err := exec.CommandContext(ctx, look, "youngest", repo).Output()
	if err != nil {
		t.Fatal(err)
	}
	type commitResult struct {
		out []byte
		err error
	}
	commitDone := make(chan commitResult, 1)
	go func() {
		out, err := call("mkdir", "-m", "interrupted capacity acceptance", url+"interrupted")
		commitDone <- commitResult{out, err}
	}()
	for {
		if _, err := os.Stat(filepath.Join(repo, "db", "commit-ready")); err == nil {
			break
		}
		select {
		case result := <-commitDone:
			t.Fatal("commit ended before the native hook", result.err, string(result.out))
		case <-ctx.Done():
			t.Fatal("commit never reached the native hook", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := gate.Hold(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SignalAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.WithAllSessionsStopped(ctx, func() error { return gate.WithExclusive(ctx, gate.AcknowledgeHeld) }); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-commitDone:
		if result.err == nil {
			t.Fatal("interrupted commit reported success")
		}
	case <-ctx.Done():
		t.Fatal("native commit did not terminate", ctx.Err())
	}
	after, err := exec.CommandContext(ctx, look, "youngest", repo).Output()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("interrupted commit changed HEAD", err)
	}
	if !manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("commit interruption revoked the activation")
	}
}
