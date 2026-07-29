package servertool

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/activation"
	"filees/pkg/deploy"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestClientEntrySeparatesProofFromForcedSVNCommand(t *testing.T) {
	if runtime.GOOS == "openbsd" && os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestClientEntrySeparatesProofFromForcedSVNCommand$")
		command.Env = append(os.Environ(), "FILEES_CLIENT_ENTRY_NATIVE=1", "FILEES_CLIENT_ENTRY_ROOT="+t.TempDir())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native client entry child: %v: %s", err, output)
		}
		return
	}
	root := os.Getenv("FILEES_CLIENT_ENTRY_ROOT")
	if root == "" {
		root = t.TempDir()
	}
	trueBinary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	activationRoot := filepath.Join(root, "activation")
	config := activation.Config{
		Root: activationRoot, AuthorizedKeysFile: filepath.Join(activationRoot, "authorized_keys"),
		AuthzFile: filepath.Join(activationRoot, "authz"), ServiceWorkingCopy: filepath.Join(root, "wc"),
		ServiceRepository: filepath.Join(root, "repository"), RepositoryName: "filees-service",
		ClientEntryPath: "/usr/local/libexec/filees/filees-client-entry", SVNBinary: trueBinary, SVNServeBinary: trueBinary,
	}
	manager, err := activation.New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.ServiceWorkingCopy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.ServiceRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(public)
	grant := onboarding.ActivationGrant{
		OperationID: uuid.NewString(), DeployRequestID: uuid.NewString(), ClientID: uuid.NewString(), RealmID: uuid.NewString(),
		InstallationPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " filees:test",
		InstallationFingerprint: ssh.FingerprintSHA256(key), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.json")
	file := map[string]any{
		"schema": serverconfig.Schema, "root": filepath.Join(root, "onboarding"), "otp_pepper_file": filepath.Join(root, "pepper"),
		"operation_ttl": "30m", "otp_attempts": 3, "reverse_port_first": 42000, "reverse_port_last": 42000,
		"activation": map[string]any{
			"root": config.Root, "authorized_keys_file": config.AuthorizedKeysFile, "authz_file": config.AuthzFile,
			"service_working_copy": config.ServiceWorkingCopy, "service_repository": config.ServiceRepository,
			"repository_name": config.RepositoryName, "client_entry_path": config.ClientEntryPath,
			"svn_binary": config.SVNBinary, "svnserve_binary": config.SVNServeBinary,
		},
		"smtp": map[string]any{"address": "127.0.0.1:2525", "client_name": "filees.test", "from": "filees@example.test", "message_id_domain": "filees.test", "tls": "none"},
	}
	raw, _ := json.Marshal(file)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") == "" {
		originalBegin, originalApply, originalPledge := sandboxBegin, sandboxApplyForExec, sandboxPledgeForExec
		t.Cleanup(func() {
			sandboxBegin, sandboxApplyForExec, sandboxPledgeForExec = originalBegin, originalApply, originalPledge
		})
		sandboxBegin = func(string) error { return nil }
		sandboxApplyForExec = func(obsandbox.Profile, string) error { return nil }
		sandboxPledgeForExec = func(string, string) error { return nil }
	}
	called := false
	supervisorCode := ExitOK
	var supervisorErr error
	supervise := func(_ serverconfig.Config, clientID string, _ *activation.Manager, _ *activation.SessionLease, _ io.Reader, _ io.Writer, _ io.Writer) (int, error) {
		called = clientID == grant.ClientID
		return supervisorCode, supervisorErr
	}
	getenv := func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return ClientSVNCommand
		}
		return ""
	}
	var stderr bytes.Buffer
	getenv = func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return deploy.ServiceProofCommand
		}
		return ""
	}
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise); code != ExitOK || called {
		t.Fatalf("proof entry code=%d called-svn=%v stderr=%s", code, called, stderr.String())
	}
	getenv = func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return ClientSVNCommand
		}
		return ""
	}
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise); code != ExitOK || !called {
		t.Fatalf("entry code=%d called=%v stderr=%s", code, called, stderr.String())
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") == "" {
		supervisorCode = 23
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise); code != 23 {
			t.Fatalf("entry replaced child exit code: got=%d want=23 stderr=%s", code, stderr.String())
		}
		supervisorCode = ExitOK
		supervisorErr = errors.New("test supervisor failure")
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise); code != ExitSoftware {
			t.Fatalf("entry supervisor failure code=%d, want=%d stderr=%s", code, ExitSoftware, stderr.String())
		}
		supervisorErr = nil

		originalControl, originalMail := runRepositoryWorkerProcess, runMailAfterControl
		defer func() { runRepositoryWorkerProcess, runMailAfterControl = originalControl, originalMail }()
		controlClient := ""
		runRepositoryWorkerProcess = func(_ string, id string, _ io.Reader, _ io.Writer, _ io.Writer) error { controlClient = id; return nil }
		mailTriggered := false
		runMailAfterControl = func(io.Writer) error { mailTriggered = true; return nil }
		getenv = func(name string) string {
			if name == "SSH_ORIGINAL_COMMAND" {
				return ClientControlCommand
			}
			return ""
		}
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise); code != ExitOK || controlClient != grant.ClientID || !mailTriggered {
			t.Fatalf("control code=%d client=%q mail=%v stderr=%s", code, controlClient, mailTriggered, stderr.String())
		}
	}
	if err := manager.HasProof(grant); err != nil {
		t.Fatalf("server proof receipt: %v", err)
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") != "" {
		return
	}
	getenv = func(string) string { return "sh" }
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise); code != ExitUnavailable {
		t.Fatalf("entry accepted arbitrary original command: %d", code)
	}
}

func TestClientSVNRootIsSeparatedByLoginAccount(t *testing.T) {
	c := serverconfig.Config{Activation: activation.Config{SVNServeBinary: "/usr/local/bin/svnserve", ServiceRepository: "/var/filees/service-repo"}, Repositories: serverconfig.RepositoryFile{Root: "/var/filees/repositories"}}
	service := clientSVNArgs(c, "client", "_filees-client")
	data := clientSVNArgs(c, "client", "_filees-data")
	if service[len(service)-1] != "/var/filees/service-repo" || data[len(data)-1] != "/var/filees/repositories" {
		t.Fatalf("service=%v data=%v", service, data)
	}
}

func TestClientSVNChildRetainsWritePromises(t *testing.T) {
	if got := clientChildPromises(ClientSVNCommand); got != svnExecPromises {
		t.Fatalf("SVN child promises = %q, want %q", got, svnExecPromises)
	}
	if got := clientChildPromises(deploy.ServiceProofCommand); got == svnExecPromises {
		t.Fatalf("proof child unexpectedly received SVN write promises: %q", got)
	}
}

func TestClientSVNBootstrapPromisesIncludeDPathForSessionFIFO(t *testing.T) {
	if !strings.Contains(" "+clientSVNEntryPromises+" ", " dpath ") {
		t.Fatalf("client SVN entry promises %q omit dpath required by mkfifo", clientSVNEntryPromises)
	}
	if strings.Contains(" "+clientEntryPromises+" ", " dpath ") {
		t.Fatalf("non-SVN client entry promises unexpectedly include dpath: %q", clientEntryPromises)
	}
}
