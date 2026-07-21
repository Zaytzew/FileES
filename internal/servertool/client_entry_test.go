package servertool

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	execute := func(_ serverconfig.Config, clientID string) error {
		called = clientID == grant.ClientID
		return nil
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
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, &stderr, getenv, execute); code != ExitOK || called {
		t.Fatalf("proof entry code=%d called-svn=%v stderr=%s", code, called, stderr.String())
	}
	getenv = func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return ClientSVNCommand
		}
		return ""
	}
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, &stderr, getenv, execute); code != ExitOK || !called {
		t.Fatalf("entry code=%d called=%v stderr=%s", code, called, stderr.String())
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") == "" {
		originalControl := execRepositoryWorker
		defer func() { execRepositoryWorker = originalControl }()
		controlClient := ""
		execRepositoryWorker = func(_ string, id string) error { controlClient = id; return nil }
		getenv = func(name string) string {
			if name == "SSH_ORIGINAL_COMMAND" {
				return ClientControlCommand
			}
			return ""
		}
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, &stderr, getenv, execute); code != ExitOK || controlClient != grant.ClientID {
			t.Fatalf("control code=%d client=%q stderr=%s", code, controlClient, stderr.String())
		}
	}
	if err := manager.HasProof(grant); err != nil {
		t.Fatalf("server proof receipt: %v", err)
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") != "" {
		return
	}
	getenv = func(string) string { return "sh" }
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, &stderr, getenv, execute); code != ExitUnavailable {
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
