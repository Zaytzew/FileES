package servertool

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/activation"
	"filees/pkg/clientview"
	"filees/pkg/onboarding"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type mobileWorkerFixture struct {
	root       string
	serviceWC  string
	configPath string
	realmID    string
}

// newMobileWorkerFixture builds a real onboarding root + activation root +
// SVN-backed service repository/working copy + server.json, mirroring
// newS3WorkerFixture but stripped of everything reverse-tunnel/deploy-helper
// specific - mobile never needs any of that (see
// concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md SS4.2).
func newMobileWorkerFixture(t *testing.T) mobileWorkerFixture {
	t.Helper()
	svnBinary, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	svnadminBinary, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	svnserveBinary, err := exec.LookPath("svnserve")
	if err != nil {
		t.Skip("svnserve unavailable")
	}

	base := t.TempDir()
	root := filepath.Join(base, "onboarding")
	if err := onboarding.Initialize(root); err != nil {
		t.Fatal(err)
	}

	serviceRepository := filepath.Join(base, "service-repo")
	serviceWC := filepath.Join(base, "service-wc")
	runWorkerTestCommand(t, svnadminBinary, "create", serviceRepository)
	runWorkerTestCommand(t, svnBinary, "mkdir", "--non-interactive", "--no-auth-cache", "-m", "filees: initialize proof", "file://"+serviceRepository+"/proof")
	runWorkerTestCommand(t, svnBinary, "checkout", "--non-interactive", "--no-auth-cache", "file://"+serviceRepository, serviceWC)

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

	pepper := bytes.Repeat([]byte{0x62}, 32)
	pepperPath := filepath.Join(base, "otp.pepper")
	if err := os.WriteFile(pepperPath, []byte(base64.StdEncoding.EncodeToString(pepper)), 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(base, "server.json")
	config := map[string]any{
		"schema": "filees.server-toolchain/v1", "root": root, "otp_pepper_file": pepperPath,
		"operation_ttl": "1h", "otp_attempts": 3,
		"reverse_port_first": 44000, "reverse_port_last": 44000,
		"activation": map[string]any{
			"root": activationRoot, "authorized_keys_file": authorizedKeysPath, "authz_file": authzPath,
			"service_working_copy": serviceWC, "service_repository": serviceRepository,
			"repository_name": "filees-service", "client_entry_path": "/usr/local/libexec/filees/filees-client-entry",
			"mobile_entry_path": "/usr/local/libexec/filees/filees-mobile-v1",
			"svn_binary":        svnBinary, "svnserve_binary": svnserveBinary,
		},
		"smtp": map[string]any{"address": "127.0.0.1:2525", "client_name": "filees.test", "from": "filees@example.test", "message_id_domain": "filees.test", "tls": "none"},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return mobileWorkerFixture{root: root, serviceWC: serviceWC, configPath: configPath, realmID: uuid.NewString()}
}

func (f mobileWorkerFixture) options() onboarding.Options {
	pepper := bytes.Repeat([]byte{0x62}, 32)
	return onboarding.Options{OTPPepper: pepper, OperationTTL: time.Hour, OTPAttempts: 3, ReversePortFirst: 44000, ReversePortLast: 44000}
}

// pair creates a mobile pairing and authenticates the token, leaving an
// operation in OperationTunnelAuthorized - exactly the state a real phone's
// SSH session would reach after BSD-Auth accepts its token.
func (f mobileWorkerFixture) pair(t *testing.T) string {
	t.Helper()
	store, err := onboarding.OpenExisting(f.root, f.options(), onboarding.Access{Areas: onboarding.AreaAll, NeedOTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, _, err := store.CreateMobilePairing(f.realmID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateOTP(token); err != nil {
		t.Fatal(err)
	}
	return token
}

func testInstallationKey(t *testing.T, comment string) (public, fingerprint string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " " + comment, ssh.FingerprintSHA256(key)
}

func TestRunMobileOnboardWorkerStagesPushedKey(t *testing.T) {
	f := newMobileWorkerFixture(t)
	f.pair(t)
	publicKey, fingerprint := testInstallationKey(t, "filees:mobile-onboard-test")

	payload, err := json.Marshal(MobileOnboardPushPayload{PublicKey: publicKey, Fingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runMobileOnboardWorker(f.configPath, bytes.NewReader(payload), &stdout, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result mobileWorkerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "staged" || result.OperationID == "" {
		t.Fatalf("result=%+v err=%v raw=%s", result, err, stdout.String())
	}

	keysRaw, err := os.ReadFile(filepath.Join(filepath.Dir(f.configPath), "activation", "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(keysRaw), "/usr/local/libexec/filees/filees-mobile-v1 "+result.OperationID+" "+result.ClientID) {
		t.Fatalf("mobile key not rendered with MobileEntryPath: %s", keysRaw)
	}
}

func TestRunMobileOnboardWorkerRejectsWithoutAnAuthorizedPairing(t *testing.T) {
	f := newMobileWorkerFixture(t)
	publicKey, fingerprint := testInstallationKey(t, "filees:no-pairing")
	payload, _ := json.Marshal(MobileOnboardPushPayload{PublicKey: publicKey, Fingerprint: fingerprint})
	var stdout, stderr bytes.Buffer
	if code := runMobileOnboardWorker(f.configPath, bytes.NewReader(payload), &stdout, &stderr); code == ExitOK {
		t.Fatalf("push without an authorized pairing was accepted: stdout=%s", stdout.String())
	}
}

// stageMobileInstallation drives the real onboarding->stage chain (pair,
// push, activation.Manager.Stage) directly, the same sequence
// runMobileOnboardWorker performs, so proof/finish tests can start from a
// realistically staged record without re-deriving it through the worker
// binary each time.
func (f mobileWorkerFixture) stageMobileInstallation(t *testing.T) (operationID, clientID string) {
	t.Helper()
	f.pair(t)
	publicKey, fingerprint := testInstallationKey(t, "filees:mobile-stage-test")
	store, err := onboarding.OpenExisting(f.root, f.options(), onboarding.Access{Areas: onboarding.AreaAll, NeedOTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	grant, err := store.ClaimAuthorizedMobilePush(publicKey, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	config, err := activationConfigFor(f)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := activation.New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	return grant.OperationID, grant.ClientID
}

func activationConfigFor(f mobileWorkerFixture) (activation.Config, error) {
	activationRoot := filepath.Join(filepath.Dir(f.configPath), "activation")
	svnBinary, err := exec.LookPath("svn")
	if err != nil {
		return activation.Config{}, err
	}
	svnserveBinary, err := exec.LookPath("svnserve")
	if err != nil {
		return activation.Config{}, err
	}
	return activation.Config{
		Root: activationRoot, AuthorizedKeysFile: filepath.Join(activationRoot, "authorized_keys"),
		AuthzFile: filepath.Join(activationRoot, "service.authz"), ServiceWorkingCopy: f.serviceWC,
		ServiceRepository: filepath.Join(filepath.Dir(f.configPath), "service-repo"), RepositoryName: "filees-service",
		ClientEntryPath: "/usr/local/libexec/filees/filees-client-entry", MobileEntryPath: "/usr/local/libexec/filees/filees-mobile-v1",
		SVNBinary: svnBinary, SVNServeBinary: svnserveBinary,
	}, nil
}

func TestRunMobileProofWorkerRecordsProof(t *testing.T) {
	f := newMobileWorkerFixture(t)
	operationID, clientID := f.stageMobileInstallation(t)

	var stdout, stderr bytes.Buffer
	if code := runMobileProofWorker(f.configPath, []string{operationID, clientID}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result mobileWorkerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "proved" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunMobileProofWorkerRejectsMismatchedClient(t *testing.T) {
	f := newMobileWorkerFixture(t)
	operationID, _ := f.stageMobileInstallation(t)
	var stdout, stderr bytes.Buffer
	if code := runMobileProofWorker(f.configPath, []string{operationID, uuid.NewString()}, &stdout, &stderr); code == ExitOK {
		t.Fatalf("mismatched client accepted: %s", stdout.String())
	}
}

func TestRunMobileFinishWorkerPublishesAfterProof(t *testing.T) {
	f := newMobileWorkerFixture(t)
	operationID, clientID := f.stageMobileInstallation(t)

	config, err := activationConfigFor(f)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := activation.New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(operationID, clientID); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runMobileFinishWorker(f.configPath, []string{operationID, clientID}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result mobileWorkerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "active" || result.ServiceRevision <= 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	view, err := clientview.Load(filepath.Join(f.serviceWC, "clients", clientID, "view.json"))
	if err != nil || view.RealmID != f.realmID || !view.CanCreateRepositories() {
		t.Fatalf("view=%+v err=%v, want realm=%s", view, err, f.realmID)
	}

	// Idempotent replay must not fail or re-publish a second revision.
	var replayOut, replayErr bytes.Buffer
	if code := runMobileFinishWorker(f.configPath, []string{operationID, clientID}, &replayOut, &replayErr); code != ExitOK {
		t.Fatalf("replay code=%d stderr=%s", code, replayErr.String())
	}
	var replay mobileWorkerResult
	if err := json.Unmarshal(replayOut.Bytes(), &replay); err != nil || replay.ServiceRevision != result.ServiceRevision {
		t.Fatalf("replay=%+v want revision=%d", replay, result.ServiceRevision)
	}
}

func TestRunMobileFinishWorkerRejectsWithoutProof(t *testing.T) {
	f := newMobileWorkerFixture(t)
	operationID, clientID := f.stageMobileInstallation(t)
	var stdout, stderr bytes.Buffer
	if code := runMobileFinishWorker(f.configPath, []string{operationID, clientID}, &stdout, &stderr); code == ExitOK {
		t.Fatalf("finish without proof was accepted: %s", stdout.String())
	}
}

func TestRunMobileWorkersRejectBadArgs(t *testing.T) {
	f := newMobileWorkerFixture(t)
	var stdout, stderr bytes.Buffer
	if code := runMobileProofWorker(f.configPath, []string{"only-one"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("proof bad args code=%d", code)
	}
	if code := runMobileFinishWorker(f.configPath, nil, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("finish bad args code=%d", code)
	}
	if code := runMobileOnboardWorker(f.configPath, bytes.NewReader([]byte("not json")), &stdout, &stderr); code != ExitUsage {
		t.Fatalf("onboard bad payload code=%d", code)
	}
}
