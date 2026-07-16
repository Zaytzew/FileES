package servertool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"filees/pkg/activation"
	"filees/pkg/deploy"
	"filees/pkg/onboarding"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestS3WorkerGeneratesIdentityThroughPinnedHelper(t *testing.T) {
	if runtime.GOOS == "openbsd" && os.Getenv("FILEES_S3_NATIVE_CHILD") == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestS3WorkerGeneratesIdentityThroughPinnedHelper$")
		command.Env = append(os.Environ(), "FILEES_S3_NATIVE_CHILD=1", "FILEES_S3_NATIVE_ROOT="+t.TempDir())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native worker child: %v: %s", err, output)
		}
		return
	}
	fixture := newS3WorkerFixture(t, "s3@example.test")
	var stdout, stderr bytes.Buffer
	if code := runWorker(fixture.configPath, []string{"deploy"}, bytes.NewReader(fixture.frame), &stdout, &stderr); code != ExitOK {
		t.Fatalf("worker exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status":"active"`) || !strings.Contains(stdout.String(), fixture.operationID) {
		t.Fatalf("worker result=%s", stdout.String())
	}
	op := fixture.operation(t)
	if op.State != onboarding.OperationActive || op.DeployRequestID != fixture.deployRequestID || op.InstallationPublicKey == "" || op.InstallationFingerprint == "" || op.ClientID == "" || op.ServiceRevision <= 0 || op.ActivatedAt == nil {
		t.Fatalf("operation=%+v", op)
	}
	if _, err := os.Stat(filepath.Join(fixture.serviceWC, "admin", "clients", op.ClientID+".json")); err != nil {
		t.Fatalf("active client projection missing: %v", err)
	}
}

func TestS4WorkerResumesEveryDurableActivationBoundary(t *testing.T) {
	fixture := newS3WorkerFixture(t, "s4-chaos@example.test")
	boundaries := []struct {
		name         string
		state        onboarding.OperationState
		stagedAccess bool
		clientView   bool
	}{
		{"helper_announced", onboarding.OperationHelperAnnounced, false, false},
		{"identity_returned", onboarding.OperationHelperAnnounced, false, false},
		{"identity_generated", onboarding.OperationIdentityGenerated, false, false},
		{"activation_staged", onboarding.OperationIdentityGenerated, true, false},
		{"access_staged", onboarding.OperationAccessStaged, true, false},
		{"possession_receipt", onboarding.OperationAccessStaged, true, false},
		{"possession_proved", onboarding.OperationPossessionProved, true, false},
		{"service_published", onboarding.OperationPossessionProved, true, true},
		{"operation_active", onboarding.OperationActive, true, true},
	}
	for _, boundary := range boundaries {
		t.Run(boundary.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestS4WorkerCrashChild$")
			command.Env = append(os.Environ(),
				"FILEES_S4_CRASH_CHECKPOINT="+boundary.name,
				"FILEES_S4_CRASH_CONFIG="+fixture.configPath,
				"FILEES_S4_CRASH_FRAME="+base64.StdEncoding.EncodeToString(fixture.frame),
			)
			output, err := command.CombinedOutput()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				t.Fatalf("checkpoint %s child was not killed: %v: %s", boundary.name, err, output)
			}
			status, ok := exitError.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("checkpoint %s exit status=%v: %s", boundary.name, exitError.Sys(), output)
			}
			if op := fixture.operation(t); op.State != boundary.state {
				t.Fatalf("checkpoint %s persisted state=%s, want %s", boundary.name, op.State, boundary.state)
			}
			fixture.assertAccess(t, boundary.stagedAccess, boundary.clientView)
		})
	}
	command := exec.Command(os.Args[0], "-test.run=^TestS4WorkerResumeChild$")
	command.Env = append(os.Environ(),
		"FILEES_S4_RESUME_CONFIG="+fixture.configPath,
		"FILEES_S4_RESUME_FRAME="+base64.StdEncoding.EncodeToString(fixture.frame),
	)
	stdout, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("final resume: %v: %s", err, stdout)
	}
	op := fixture.operation(t)
	if op.State != onboarding.OperationActive || op.ServiceRevision != 2 || !strings.Contains(string(stdout), `"status":"active"`) {
		t.Fatalf("final operation=%+v stdout=%s", op, stdout)
	}
	fixture.assertAccess(t, true, true)
	for _, event := range []string{"tunnel_session_started", "helper_announced", "helper_verified", "identity_generated", "access_staged", "possession_verified", "client_activated"} {
		count := 0
		for _, entry := range fixture.bundle(t).Audit {
			if entry.Event == event {
				count++
			}
		}
		if count != 1 {
			t.Errorf("audit event %s count=%d, want 1", event, count)
		}
	}
}

func TestS4ExpiredStagingRecoveryIsFailClosed(t *testing.T) {
	t.Setenv("FILEES_S4_OPERATION_TTL", "1s")
	fixture := newS3WorkerFixture(t, "s4-expired-staging@example.test")
	command := exec.Command(os.Args[0], "-test.run=^TestS4WorkerCrashChild$")
	command.Env = append(os.Environ(),
		"FILEES_S4_CRASH_CHECKPOINT=activation_staged",
		"FILEES_S4_CRASH_CONFIG="+fixture.configPath,
		"FILEES_S4_CRASH_FRAME="+base64.StdEncoding.EncodeToString(fixture.frame),
	)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("staging child was not killed: %v: %s", err, output)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("staging child exit status=%v: %s", exitError.Sys(), output)
	}
	fixture.assertAccess(t, true, false)
	time.Sleep(1100 * time.Millisecond)
	recoverCommand := exec.Command(os.Args[0], "-test.run=^TestS4RecoverAfterPowerLoss$")
	recoverCommand.Env = append(os.Environ(), "FILEES_S4_POWER_LOSS_RECOVER="+fixture.configPath)
	if output, err := recoverCommand.CombinedOutput(); err != nil {
		t.Fatalf("expired staging recovery: %v: %s", err, output)
	}
	if op := fixture.operation(t); op.State != onboarding.OperationExpired {
		t.Fatalf("recovered operation state=%s, want expired", op.State)
	}
	fixture.assertAccess(t, false, false)
}

func TestS4WorkerResumeChild(t *testing.T) {
	configPath := os.Getenv("FILEES_S4_RESUME_CONFIG")
	if configPath == "" {
		return
	}
	frame, err := base64.StdEncoding.DecodeString(os.Getenv("FILEES_S4_RESUME_FRAME"))
	if err != nil {
		t.Fatal(err)
	}
	if code := runWorker(configPath, []string{"deploy"}, bytes.NewReader(frame), os.Stdout, os.Stderr); code != ExitOK {
		os.Exit(code)
	}
}

func TestS4PreparePowerLoss(t *testing.T) {
	if os.Getenv("FILEES_S4_POWER_LOSS_PREPARE") == "" {
		return
	}
	fixture := newS3WorkerFixture(t, "s4-power-loss@example.test")
	readyPath := filepath.Join(fixture.root, "operations", ".s4-power-loss-ready")
	command := exec.Command(os.Args[0], "-test.run=^TestS4PowerLossWorkerChild$")
	command.Env = append(os.Environ(),
		"FILEES_S4_POWER_LOSS_CONFIG="+fixture.configPath,
		"FILEES_S4_POWER_LOSS_FRAME="+base64.StdEncoding.EncodeToString(fixture.frame),
		"FILEES_S4_POWER_LOSS_READY="+readyPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("power-loss worker stopped before VM shutdown: %v: %s", err, output)
	}
	t.Fatal("power-loss worker returned without VM shutdown")
}

func TestS4PowerLossWorkerChild(t *testing.T) {
	configPath := os.Getenv("FILEES_S4_POWER_LOSS_CONFIG")
	if configPath == "" {
		return
	}
	frame, err := base64.StdEncoding.DecodeString(os.Getenv("FILEES_S4_POWER_LOSS_FRAME"))
	if err != nil {
		t.Fatal(err)
	}
	holdAtStagedAccess := func(name string) error {
		if name != "activation_staged" {
			return nil
		}
		readyPath := os.Getenv("FILEES_S4_POWER_LOSS_READY")
		if err := os.WriteFile(readyPath, []byte("activation_staged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	var stdout, stderr bytes.Buffer
	code := runWorkerWithCheckpoint(configPath, []string{"deploy"}, bytes.NewReader(frame), &stdout, &stderr, holdAtStagedAccess)
	t.Fatalf("power-loss checkpoint was not held: exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
}

func TestS4RecoverAfterPowerLoss(t *testing.T) {
	configPath := os.Getenv("FILEES_S4_POWER_LOSS_RECOVER")
	if configPath == "" {
		return
	}
	var stdout, stderr bytes.Buffer
	if code := RunOperation([]string{"-config", configPath, "recover"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("recovery exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	root := filepath.Join(filepath.Dir(configPath), "onboarding")
	paths, err := filepath.Glob(filepath.Join(root, "operations", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("operation files=%v err=%v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var bundle onboarding.Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Operation.State != onboarding.OperationExpired {
		t.Fatalf("operation state=%s, want expired", bundle.Operation.State)
	}
	keys, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), "activation", "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(keys), bundle.Operation.ClientID) {
		t.Fatalf("expired activation retained runtime key: %s", keys)
	}
}

func TestS4WorkerCrashChild(t *testing.T) {
	checkpoint := os.Getenv("FILEES_S4_CRASH_CHECKPOINT")
	if checkpoint == "" {
		return
	}
	frame, err := base64.StdEncoding.DecodeString(os.Getenv("FILEES_S4_CRASH_FRAME"))
	if err != nil {
		t.Fatal(err)
	}
	killAtCheckpoint := func(name string) error {
		if name != checkpoint {
			return nil
		}
		process, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Kill(); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	var stdout, stderr bytes.Buffer
	code := runWorkerWithCheckpoint(os.Getenv("FILEES_S4_CRASH_CONFIG"), []string{"deploy"}, bytes.NewReader(frame), &stdout, &stderr, killAtCheckpoint)
	t.Fatalf("worker passed crash checkpoint %s: exit=%d stdout=%s stderr=%s", checkpoint, code, stdout.String(), stderr.String())
}

type s3WorkerFixture struct {
	root, serviceWC, configPath, deployRequestID, operationID, onboardingRequestID string
	authorizedKeysPath, authzPath                                                  string
	frame                                                                          []byte
}

func newS3WorkerFixture(t *testing.T, email string) s3WorkerFixture {
	t.Helper()
	base := os.Getenv("FILEES_S3_NATIVE_ROOT")
	if base == "" {
		base = t.TempDir()
	}
	root := filepath.Join(base, "onboarding")
	if err := onboarding.Initialize(root); err != nil {
		t.Fatal(err)
	}
	_, workerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	workerSigner, err := ssh.NewSignerFromKey(workerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	fakeSVN := os.Getenv("FILEES_S3_FAKE_SVN")
	var svnBinary, svnserveBinary string
	serviceRepository := filepath.Join(base, "service-repo")
	serviceWC := filepath.Join(base, "service-wc")
	if fakeSVN != "" {
		if !filepath.IsAbs(fakeSVN) {
			t.Fatal("FILEES_S3_FAKE_SVN must be absolute")
		}
		svnBinary, svnserveBinary = fakeSVN, fakeSVN
		if err := os.MkdirAll(filepath.Join(serviceWC, "proof"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(serviceRepository, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(serviceWC, ".filees-fake-svn-wc"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		var err error
		svnBinary, err = exec.LookPath("svn")
		if err != nil {
			t.Fatal("svn is required for S3 activation integration")
		}
		svnadminBinary, err := exec.LookPath("svnadmin")
		if err != nil {
			t.Fatal("svnadmin is required for S3 activation integration")
		}
		svnserveBinary, err = exec.LookPath("svnserve")
		if err != nil {
			t.Fatal("svnserve is required for S3 activation integration")
		}
		runWorkerTestCommand(t, svnadminBinary, "create", serviceRepository)
		runWorkerTestCommand(t, svnBinary, "mkdir", "--non-interactive", "--no-auth-cache", "-m", "filees: initialize proof", "file://"+serviceRepository+"/proof")
		runWorkerTestCommand(t, svnBinary, "checkout", "--non-interactive", "--no-auth-cache", "file://"+serviceRepository, serviceWC)
	}
	activationRoot := filepath.Join(base, "activation")
	if err := os.MkdirAll(activationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	authorizedKeysPath := filepath.Join(activationRoot, "authorized_keys")
	authzPath := filepath.Join(activationRoot, "service.authz")
	if err := os.WriteFile(authorizedKeysPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authzPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	activationConfig := activation.Config{
		Root: activationRoot, AuthorizedKeysFile: authorizedKeysPath,
		AuthzFile: authzPath, ServiceWorkingCopy: serviceWC, ServiceRepository: serviceRepository,
		RepositoryName: "filees-service", ClientEntryPath: "/usr/local/libexec/filees/filees-client-entry",
		SVNBinary: svnBinary, SVNServeBinary: svnserveBinary,
	}
	activationManager, err := activation.New(activationConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := deploy.StartHelper(context.Background(), deploy.HelperConfig{
		WorkerKey: workerSigner.PublicKey(),
		Identity:  deploy.IdentityGenerator{Root: filepath.Join(root, "operations", "client-identity")},
		Access:    activationReceiptProver{manager: activationManager},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Close() })
	_, portText, _ := net.SplitHostPort(helper.Endpoint().Address)
	portNumber, _ := strconv.Atoi(portText)
	port := uint16(portNumber)

	operationTTL := time.Minute
	if rawTTL := os.Getenv("FILEES_S4_OPERATION_TTL"); rawTTL != "" {
		operationTTL, err = time.ParseDuration(rawTTL)
		if err != nil {
			t.Fatal(err)
		}
	}
	pepper := bytes.Repeat([]byte{0x51}, 32)
	options := onboarding.Options{OTPPepper: pepper, OperationTTL: operationTTL, OTPAttempts: 3, ReversePortFirst: port, ReversePortLast: port}
	store, err := onboarding.OpenExisting(root, options, onboarding.Access{Areas: onboarding.AreaAll, NeedOTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTicket(email, onboarding.Policy{RealmID: uuid.NewString()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Take(email, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := store.ListOutbox()
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox=%v err=%v", outbox, err)
	}
	if _, err := store.AuthenticateOTP(outbox[0].OTP); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	workerKeyPath := filepath.Join(base, "worker_ed25519")
	workerPublicPath := filepath.Join(base, "worker_ed25519.pub")
	block, err := ssh.MarshalPrivateKey(workerPrivate, "filees-worker-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerPublicPath, ssh.MarshalAuthorizedKey(workerSigner.PublicKey()), 0o644); err != nil {
		t.Fatal(err)
	}
	pepperPath := filepath.Join(base, "otp.pepper")
	if err := os.WriteFile(pepperPath, []byte(base64.StdEncoding.EncodeToString(pepper)), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, "server.json")
	config := map[string]any{
		"schema": "filees.server-toolchain/v1", "root": root, "otp_pepper_file": pepperPath,
		"worker_private_key_file": workerKeyPath, "worker_public_key_file": workerPublicPath, "operation_ttl": operationTTL.String(), "otp_attempts": 3,
		"reverse_port_first": port, "reverse_port_last": port,
		"activation": map[string]any{
			"root": activationConfig.Root, "authorized_keys_file": authorizedKeysPath, "authz_file": authzPath,
			"service_working_copy": serviceWC, "service_repository": serviceRepository,
			"repository_name": "filees-service", "client_entry_path": activationConfig.ClientEntryPath,
			"svn_binary": svnBinary, "svnserve_binary": svnserveBinary,
		},
		"smtp": map[string]any{"address": "127.0.0.1:2525", "client_name": "filees.test", "from": "filees@example.test", "message_id_domain": "filees.test", "tls": "none"},
	}
	rawConfig, _ := json.Marshal(config)
	if err := os.WriteFile(configPath, rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	deployRequestID := uuid.NewString()
	frame, err := deploy.EncodeTunnelSession(deploy.TunnelSession{Schema: deploy.TunnelSessionSchema, DeployRequestID: deployRequestID, HelperHostPublicKey: helper.Endpoint().HostPublicKey})
	if err != nil {
		t.Fatal(err)
	}
	return s3WorkerFixture{
		root: root, serviceWC: serviceWC, configPath: configPath, deployRequestID: deployRequestID,
		operationID: receipt.OperationID, onboardingRequestID: receipt.OnboardingRequestID,
		authorizedKeysPath: authorizedKeysPath, authzPath: authzPath, frame: frame,
	}
}

func (f s3WorkerFixture) operation(t *testing.T) onboarding.Operation {
	t.Helper()
	return f.bundle(t).Operation
}

func (f s3WorkerFixture) bundle(t *testing.T) onboarding.Bundle {
	t.Helper()
	rawOperation, err := os.ReadFile(filepath.Join(f.root, "operations", f.onboardingRequestID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var completed onboarding.Bundle
	if err := json.Unmarshal(rawOperation, &completed); err != nil {
		t.Fatal(err)
	}
	return completed
}

func (f s3WorkerFixture) assertAccess(t *testing.T, staged, clientView bool) {
	t.Helper()
	op := f.operation(t)
	keys, err := os.ReadFile(f.authorizedKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	keyCount := strings.Count(string(keys), "filees:"+op.ClientID)
	if staged && keyCount != 1 {
		t.Fatalf("staged access key count=%d, want 1: %s", keyCount, keys)
	}
	if !staged && keyCount != 0 {
		t.Fatalf("unstaged operation has runtime key: %s", keys)
	}
	authz, err := os.ReadFile(f.authzPath)
	if err != nil {
		t.Fatal(err)
	}
	view := "[/clients/" + op.ClientID + "]"
	present := strings.Contains(string(authz), view)
	if present != clientView {
		t.Fatalf("client view access=%v, want %v: %s", present, clientView, authz)
	}
}

type activationReceiptProver struct{ manager *activation.Manager }

func (p activationReceiptProver) ProveServiceAccess(operationID, clientID string) error {
	return p.manager.RecordProof(operationID, clientID)
}

func runWorkerTestCommand(t *testing.T, command string, args ...string) {
	t.Helper()
	cmd := exec.Command(command, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", command, args, err, output)
	}
}

func TestEntryExecutesOnlyWorkerForExactForcedCommand(t *testing.T) {
	called := false
	env := func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return TunnelCommand
		}
		return ""
	}
	if code := runEntry(nil, &bytes.Buffer{}, env, func() error { called = true; return nil }); code != ExitOK || !called {
		t.Fatalf("entry code=%d called=%v", code, called)
	}
}

func TestWorkerRejectsMismatchedConfiguredKeypair(t *testing.T) {
	_, firstPrivate, _ := ed25519.GenerateKey(rand.Reader)
	_, secondPrivate, _ := ed25519.GenerateKey(rand.Reader)
	first, _ := ssh.NewSignerFromKey(firstPrivate)
	second, _ := ssh.NewSignerFromKey(secondPrivate)
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(first.PublicKey())))
	if !workerKeypairMatches(public, first) {
		t.Fatal("matching worker keypair was rejected")
	}
	if workerKeypairMatches(public, second) {
		t.Fatal("mismatched worker keypair was accepted")
	}
}
