package servertool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

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
	helper, err := deploy.StartHelper(context.Background(), deploy.HelperConfig{
		WorkerKey: workerSigner.PublicKey(),
		Identity:  deploy.IdentityGenerator{Root: filepath.Join(root, "operations", "client-identity")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer helper.Close()
	_, portText, _ := net.SplitHostPort(helper.Endpoint().Address)
	portNumber, _ := strconv.Atoi(portText)
	port := uint16(portNumber)

	pepper := bytes.Repeat([]byte{0x51}, 32)
	options := onboarding.Options{OTPPepper: pepper, OperationTTL: time.Minute, OTPAttempts: 3, ReversePortFirst: port, ReversePortLast: port}
	store, err := onboarding.OpenExisting(root, options, onboarding.Access{Areas: onboarding.AreaAll, NeedOTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTicket("s3@example.test", onboarding.Policy{RealmID: uuid.NewString()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Take("s3@example.test", uuid.NewString())
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
		"worker_private_key_file": workerKeyPath, "worker_public_key_file": workerPublicPath, "operation_ttl": "1m", "otp_attempts": 3,
		"reverse_port_first": port, "reverse_port_last": port,
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
	var stdout, stderr bytes.Buffer
	if code := runWorker(configPath, []string{"deploy"}, bytes.NewReader(frame), &stdout, &stderr); code != ExitOK {
		t.Fatalf("worker exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status":"identity_generated"`) || !strings.Contains(stdout.String(), receipt.OperationID) {
		t.Fatalf("worker result=%s", stdout.String())
	}
	rawOperation, err := os.ReadFile(filepath.Join(root, "operations", receipt.OnboardingRequestID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var completed onboarding.Bundle
	if err := json.Unmarshal(rawOperation, &completed); err != nil {
		t.Fatal(err)
	}
	op := completed.Operation
	if op.State != onboarding.OperationIdentityGenerated || op.DeployRequestID != deployRequestID || op.InstallationPublicKey == "" || op.InstallationFingerprint == "" || op.ClientID == "" {
		t.Fatalf("operation=%+v", op)
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
