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
	permitRepeatedSandbox(t)
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
		ServerDisplayName: "Serwer testowy",
		Root:              activationRoot, AuthorizedKeysFile: filepath.Join(activationRoot, "authorized_keys"),
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
		"schema": serverconfig.Schema, "display_name": "Serwer testowy", "root": filepath.Join(root, "onboarding"), "otp_pepper_file": filepath.Join(root, "pepper"),
		"operation_ttl": "30m", "otp_attempts": 3, "reverse_port_first": 42000, "reverse_port_last": 42000,
		"activation": map[string]any{
			"root": config.Root, "authorized_keys_file": config.AuthorizedKeysFile, "authz_file": config.AuthzFile,
			"service_working_copy": config.ServiceWorkingCopy, "service_repository": config.ServiceRepository,
			"repository_name": config.RepositoryName, "client_entry_path": config.ClientEntryPath,
			"svn_binary": config.SVNBinary, "svnserve_binary": config.SVNServeBinary,
		},
		"repositories": map[string]any{
			"root": filepath.Join(root, "repositories"), "results_root": filepath.Join(root, "results"),
			"data_authz_file": filepath.Join(activationRoot, "repositories.authz"), "svnadmin_binary": trueBinary,
			"url_prefix": "svn+ssh://_filees-data@filees.test/", "deletion_archive_root": filepath.Join(root, "deleted"),
			"recovery_admin_contact": "filees-admin@example.test",
		},
		"invitation": map[string]any{
			"server_id": "client-entry-test", "server_address": "filees.test:2222",
			"known_host": "[filees.test]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		"smtp": map[string]any{"address": "127.0.0.1:2525", "client_name": "filees.test", "from": "filees@example.test", "message_id_domain": "filees.test", "tls": "none"},
	}
	raw, _ := json.Marshal(file)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") == "" {
		originalBegin, originalNarrow, originalApply, originalPledge := sandboxBegin, sandboxNarrow, sandboxApplyForExec, sandboxPledgeForExec
		t.Cleanup(func() {
			sandboxBegin, sandboxNarrow, sandboxApplyForExec, sandboxPledgeForExec = originalBegin, originalNarrow, originalApply, originalPledge
		})
		sandboxBegin = func(string) error { return nil }
		sandboxNarrow = func(string) error { return nil }
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
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitOK || called {
		t.Fatalf("proof entry code=%d called-svn=%v stderr=%s", code, called, stderr.String())
	}
	getenv = func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return ClientSVNCommand
		}
		return ""
	}
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitOK || !called {
		t.Fatalf("entry code=%d called=%v stderr=%s", code, called, stderr.String())
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") == "" {
		supervisorCode = 23
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != 23 {
			t.Fatalf("entry replaced child exit code: got=%d want=23 stderr=%s", code, stderr.String())
		}
		supervisorCode = ExitOK
		supervisorErr = errors.New("test supervisor failure")
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitSoftware {
			t.Fatalf("entry supervisor failure code=%d, want=%d stderr=%s", code, ExitSoftware, stderr.String())
		}
		supervisorErr = nil

		originalCorrector, originalControl, originalMail := runOwnershipCorrectorProcess, runRepositoryWorkerProcess, runMailAfterControl
		defer func() {
			runOwnershipCorrectorProcess, runRepositoryWorkerProcess, runMailAfterControl = originalCorrector, originalControl, originalMail
		}()
		correctorRuns := 0
		runOwnershipCorrectorProcess = func(io.Writer) error { correctorRuns++; return nil }
		controlClient := ""
		runRepositoryWorkerProcess = func(_, _, id string, _ io.Reader, _ io.Writer, _ io.Writer) error { controlClient = id; return nil }
		mailTriggered := false
		runMailAfterControl = func(serverconfig.Config, io.Writer) error { mailTriggered = true; return nil }
		whaleClient := ""
		whaleSupervisor := func(_ serverconfig.Config, clientID string, _ *activation.Manager, _ *activation.SessionLease, _ io.Reader, _ io.Writer, _ io.Writer) (int, error) {
			whaleClient = clientID
			return ExitOK, nil
		}
		getenv = func(name string) string {
			if name == "SSH_ORIGINAL_COMMAND" {
				return ClientWhaleCommand
			}
			return ""
		}
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, whaleSupervisor); code != ExitOK || whaleClient != grant.ClientID || correctorRuns != 1 {
			t.Fatalf("Whale code=%d client=%q corrector=%d stderr=%s", code, whaleClient, correctorRuns, stderr.String())
		}
		correctorRuns = 0
		getenv = func(name string) string {
			if name == "SSH_ORIGINAL_COMMAND" {
				return ClientControlCommand
			}
			return ""
		}
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitOK || controlClient != grant.ClientID || !mailTriggered || correctorRuns != 1 {
			t.Fatalf("control code=%d client=%q mail=%v corrector=%d stderr=%s", code, controlClient, mailTriggered, correctorRuns, stderr.String())
		}

		originalReservation := runReservationWorkerProcess
		defer func() { runReservationWorkerProcess = originalReservation }()
		reservationClient := ""
		runReservationWorkerProcess = func(id string, _ io.Reader, _ io.Writer, _ io.Writer) error { reservationClient = id; return nil }
		getenv = func(name string) string {
			if name == "SSH_ORIGINAL_COMMAND" {
				return ClientReservationCommand
			}
			return ""
		}
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitOK || reservationClient != grant.ClientID {
			t.Fatalf("reservation code=%d client=%q stderr=%s", code, reservationClient, stderr.String())
		}
		reservationErr := errors.New("test reservation worker failure")
		runReservationWorkerProcess = func(string, io.Reader, io.Writer, io.Writer) error { return reservationErr }
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitSoftware {
			t.Fatalf("reservation worker failure code=%d, want=%d", code, ExitSoftware)
		}
		runReservationWorkerProcess = func(id string, _ io.Reader, _ io.Writer, _ io.Writer) error { reservationClient = id; return nil }
		getenv = func(name string) string {
			if name == "SSH_ORIGINAL_COMMAND" {
				return ClientControlCommand
			}
			return ""
		}

		// Ownership correction is a hard gate. A control worker must never touch
		// the service WC after the privileged repair failed, and the auxiliary
		// mail pass must not run without a durable control result.
		correctorRuns = 0
		controlClient = ""
		mailTriggered = false
		stderr.Reset()
		runOwnershipCorrectorProcess = func(io.Writer) error {
			correctorRuns++
			return errors.New("test ownership correction failure")
		}
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitSoftware || correctorRuns != 1 || controlClient != "" || mailTriggered {
			t.Fatalf("failed corrector code=%d client=%q mail=%v corrector=%d stderr=%s", code, controlClient, mailTriggered, correctorRuns, stderr.String())
		}
		if !strings.Contains(stderr.String(), "service-WC ownership") {
			t.Fatalf("failed corrector was not reported: %s", stderr.String())
		}
		runOwnershipCorrectorProcess = func(io.Writer) error { correctorRuns++; return nil }

		// Preparing SMTP is deliberately auxiliary: a missing secret must not
		// suppress or replace the durable control result.
		smtpFile := file["smtp"].(map[string]any)
		smtpFile["username"] = "mailer"
		smtpFile["password_file"] = filepath.Join(root, "missing-smtp-password")
		smtpFile["tls"] = "starttls"
		smtpFile["server_name"] = "mail.example.test"
		raw, _ = json.Marshal(file)
		if err := os.WriteFile(configPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		mailTriggered = false
		stderr.Reset()
		if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitOK || mailTriggered {
			t.Fatalf("control with mail preparation failure code=%d mail=%v stderr=%s", code, mailTriggered, stderr.String())
		}
		if !strings.Contains(stderr.String(), "filees-client-entry mail trigger") {
			t.Fatalf("mail preparation failure not reported: %s", stderr.String())
		}
	}
	if err := manager.HasProof(grant); err != nil {
		t.Fatalf("server proof receipt: %v", err)
	}
	if os.Getenv("FILEES_CLIENT_ENTRY_NATIVE") != "" {
		return
	}
	getenv = func(string) string { return "sh" }
	if code := runClientEntry(configPath, []string{grant.OperationID, grant.ClientID}, strings.NewReader(""), io.Discard, &stderr, getenv, supervise, supervise); code != ExitUnavailable {
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
	if got := clientChildPromises(ClientWhaleCommand); got != whaleExecPromises {
		t.Fatalf("Whale child promises = %q, want %q", got, whaleExecPromises)
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

func TestWhaleWorkerProfileIsClosedToRequiredTrees(t *testing.T) {
	serverConfigPath := filepath.FromSlash("/etc/filees/server.json")
	serviceWC := filepath.FromSlash("/srv/filees/service-wc")
	repositoriesRoot := filepath.FromSlash("/srv/filees/repositories")
	resultsRoot := filepath.FromSlash("/srv/filees/results")
	svnadmin := filepath.FromSlash("/usr/local/bin/svnadmin")
	config := serverconfig.Config{
		Activation: serverconfig.Config{}.Activation,
		Repositories: serverconfig.RepositoryFile{
			Root: repositoriesRoot, ResultsRoot: resultsRoot, SVNAdminBinary: svnadmin,
		},
	}
	config.Activation.ServiceWorkingCopy = serviceWC
	profile := whaleWorkerProfile(config, serverConfigPath, filepath.Join(resultsRoot, "whale"))
	paths := map[string]obsandbox.Path{}
	for _, item := range profile.Paths {
		paths[item.Label] = item
	}
	for label, want := range map[string]obsandbox.Path{
		"service-working-copy": {Label: "service-working-copy", Name: serviceWC, Perms: "r"},
		"repository-root":      {Label: "repository-root", Name: repositoriesRoot, Perms: "rwc"},
		"whale-operational":    {Label: "whale-operational", Name: filepath.Join(resultsRoot, "whale"), Perms: "rwc"},
		"svnmucc":              {Label: "svnmucc", Name: filepath.Join(filepath.Dir(svnadmin), "svnmucc"), Perms: "rx"},
		"svnlook":              {Label: "svnlook", Name: filepath.Join(filepath.Dir(svnadmin), "svnlook"), Perms: "rx"},
		"svnadmin":             {Label: "svnadmin", Name: svnadmin, Perms: "rx"},
	} {
		if got := paths[label]; got != want {
			t.Fatalf("Whale sandbox path %s = %+v, want %+v", label, got, want)
		}
	}
	if profile.Promises != whaleWorkerPromises || strings.Contains(" "+profile.Promises+" ", " inet ") {
		t.Fatalf("Whale runtime promises are too broad: %q", profile.Promises)
	}
}

func TestMailAfterControlNarrowsLockedParentSandbox(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := onboarding.Initialize(root); err != nil {
		t.Fatal(err)
	}
	originalNarrow := sandboxNarrow
	t.Cleanup(func() { sandboxNarrow = originalNarrow })
	var promises string
	sandboxNarrow = func(got string) error {
		promises = got
		return nil
	}
	config := serverconfig.Config{Root: root, Onboarding: onboarding.Options{
		OperationTTL: time.Minute, OTPAttempts: 1,
		ReversePortFirst: 42000, ReversePortLast: 42000,
	}}
	if err := deliverMailAfterControl(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if promises != mailPromises {
		t.Fatalf("mail sandbox promises = %q, want %q", promises, mailPromises)
	}
}

func TestMailAfterControlStopsWhenSandboxCannotNarrow(t *testing.T) {
	originalNarrow := sandboxNarrow
	t.Cleanup(func() { sandboxNarrow = originalNarrow })
	sandboxNarrow = func(string) error { return errors.New("pledge failed") }
	err := deliverMailAfterControl(serverconfig.Config{Root: t.TempDir()}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "narrow sandbox for mail") {
		t.Fatalf("sandbox failure = %v", err)
	}
}
