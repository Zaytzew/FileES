package servertool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"filees/pkg/deploy"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
	"filees/pkg/smtpsubmit"
)

func TestS1FilesystemWorkflow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	if err := onboarding.Initialize(root); err != nil {
		t.Fatal(err)
	}
	pepperPath := filepath.Join(filepath.Dir(root), "pepper")
	pepper := bytes.Repeat([]byte{0x42}, 32)
	if err := os.WriteFile(pepperPath, []byte(base64.StdEncoding.EncodeToString(pepper)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(filepath.Dir(root), "server.json")
	workerPublicPath := filepath.Join(filepath.Dir(root), "worker_ed25519.pub")
	workerPublic, err := deploy.BootstrapAuthorizedKey()
	if err != nil || os.WriteFile(workerPublicPath, []byte(workerPublic+"\n"), 0o644) != nil {
		t.Fatal("write worker public key")
	}
	configJSON := `{
  "schema":"filees.server-toolchain/v1",
  "root":` + quote(root) + `,
  "otp_pepper_file":` + quote(pepperPath) + `,
  "worker_public_key_file":` + quote(workerPublicPath) + `,
  "operation_ttl":"30m",
  "otp_attempts":3,
  "reverse_port_first":42000,
  "reverse_port_last":42010,
  "smtp":{"address":"127.0.0.1:2525","client_name":"filees.test","from":"filees@example.test","message_id_domain":"filees.test","tls":"none"}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := RunAdmin([]string{"-config", configPath, "ticket", "create", "-email", "alice@example.test", "-realm-id", "7b807185-aa75-4169-8a65-705c7cbab176", "-ttl", "1h"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("ticket create exit=%d stderr=%s", code, stderr.String())
	}

	requestID := uuid.NewString()
	request, _ := json.Marshal(onboarding.OnboardRequest{Schema: onboarding.OnboardRequestSchema, Email: "alice@example.test", OnboardingRequestID: requestID})
	stdout.Reset()
	stderr.Reset()
	code = RunOnboard([]string{"-config", configPath, "take"}, bytes.NewReader(request), &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("take exit=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = RunOnboard([]string{"-config", configPath, "take"}, bytes.NewReader(request), &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("idempotent take exit=%d stderr=%s", code, stderr.String())
	}

	originalSubmit := smtpSubmit
	t.Cleanup(func() { smtpSubmit = originalSubmit })
	var submitted smtpsubmit.Request
	smtpSubmit = func(_ context.Context, _ smtpsubmit.Config, request smtpsubmit.Request) error {
		submitted = request
		return nil
	}
	stdout.Reset()
	stderr.Reset()
	code = RunMail([]string{"-config", configPath, "send"}, &stdout, &stderr)
	if code != ExitOK || !strings.Contains(stdout.String(), `"status":"queued"`) {
		t.Fatalf("mail exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if submitted.Recipient != "alice@example.test" || !bytes.Contains(submitted.Message, []byte("FileES onboarding code")) {
		t.Fatalf("unexpected SMTP request: recipient=%q message=%q", submitted.Recipient, submitted.Message)
	}

	config, err := serverconfig.LoadFor(configPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	store, err := onboarding.OpenExisting(root, config.Onboarding, onboarding.Access{Areas: onboarding.AreaOperations})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox()
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox=%v err=%v", entries, err)
	}
	if entries[0].DeliveryState != onboarding.DeliveryQueued || entries[0].DeliveryAddress != "" || entries[0].OTP != "" {
		t.Fatalf("queued outbox retained delivery secret: %+v", entries[0])
	}
}

func quote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
