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
	"time"

	"github.com/google/uuid"

	"filees/pkg/deploy"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
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
	  "invitation":{"server_id":"office","server_address":"filees.test:2222","known_host":"[filees.test]:2222 ` + workerPublic + `"},
  "smtp":{"address":"127.0.0.1:2525","client_name":"filees.test","from":"filees@example.test","message_id_domain":"filees.test","tls":"none"}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	originalSubmit := smtpSubmit
	t.Cleanup(func() { smtpSubmit = originalSubmit })
	var submitted []smtpsubmit.Request
	smtpSubmit = func(_ context.Context, _ smtpsubmit.Config, request smtpsubmit.Request) error {
		submitted = append(submitted, request)
		return nil
	}
	code := RunAdmin([]string{"-config", configPath, "ticket", "create", "alice@example.test", "-ttl", "1h"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("ticket create exit=%d stderr=%s", code, stderr.String())
	}
	if len(submitted) != 1 || submitted[0].Recipient != "alice@example.test" || !bytes.Contains(submitted[0].Message, []byte("FileES activation invitation")) {
		t.Fatalf("ticket create did not deliver invitation: %+v", submitted)
	}

	requestID := uuid.NewString()
	request, _ := json.Marshal(onboarding.OnboardRequest{Schema: onboarding.LegacyOnboardRequestSchema, Email: "alice@example.test", OnboardingRequestID: requestID})
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

	stdout.Reset()
	stderr.Reset()
	code = RunMail([]string{"-config", configPath, "send"}, &stdout, &stderr)
	if code != ExitOK || !strings.Contains(stdout.String(), `"status":"queued"`) {
		t.Fatalf("mail exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if len(submitted) != 2 || submitted[1].Recipient != "alice@example.test" || !bytes.Contains(submitted[1].Message, []byte("FileES onboarding code")) {
		t.Fatalf("unexpected SMTP requests: %+v", submitted)
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

func TestRealmRemovalMailUsesSharedSMTPWorker(t *testing.T) {
	results := filepath.Join(t.TempDir(), "results")
	store := repoworker.RealmRemovalStore{Root: filepath.Join(results, "realm-removals"), OTPPepper: bytes.Repeat([]byte{0x42}, 32), TTL: time.Hour, Attempts: 3}
	record, otp, err := store.Begin(uuid.NewString(), repoworker.RealmRemovalScope{}, repoworker.RealmRemovalRequest{NotificationEmail: "remove@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	originalSubmit := smtpSubmit
	t.Cleanup(func() { smtpSubmit = originalSubmit })
	var submitted smtpsubmit.Request
	smtpSubmit = func(_ context.Context, _ smtpsubmit.Config, request smtpsubmit.Request) error {
		submitted = request
		return nil
	}
	var stdout, stderr bytes.Buffer
	config := serverconfig.Config{Repositories: serverconfig.RepositoryFile{ResultsRoot: results}, SMTPFrom: "filees@example.test", MessageIDDomain: "filees.test", SMTP: smtpsubmit.Config{Address: "127.0.0.1:2525", ClientName: "filees.test", TLSMode: smtpsubmit.TLSNone}}
	if code := deliverPendingRealmRemovalMail(config, &stdout, &stderr); code != ExitOK {
		t.Fatalf("mail exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if submitted.Recipient != "remove@example.test" || !bytes.Contains(submitted.Message, []byte(otp)) || !bytes.Contains(submitted.Message, []byte("If this was not you")) {
		t.Fatalf("unexpected realm removal message: %+v", submitted)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root, "outbox", record.OperationID+".json"))
	containsOTP := bytes.Contains(raw, []byte(otp))
	containsAddress := bytes.Contains(raw, []byte("remove@example.test"))
	queued := bytes.Contains(raw, []byte(`"delivery_state": "queued"`))
	if err != nil || containsOTP || containsAddress || !queued {
		t.Fatalf("realm removal outbox not scrubbed: otp=%v address=%v queued=%v raw=%s err=%v", containsOTP, containsAddress, queued, raw, err)
	}
}

func TestDataErasureCompletionMailUsesSharedTriggeredWorker(t *testing.T) {
	results := filepath.Join(t.TempDir(), "results")
	if err := os.MkdirAll(filepath.Join(results, "realm-removals"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := repoworker.DataErasureStore{Root: filepath.Join(results, "data-erasure"), Now: func() time.Time { return now }}
	removal := repoworker.RealmRemovalRecord{
		OperationID: uuid.NewString(), RealmID: uuid.NewString(), ConfirmedAt: &now,
		Request: repoworker.RealmRemovalRequest{NotificationEmail: "erase@example.test", ErasureRequested: true},
	}
	if _, err := store.Accept(removal, 90); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkActiveDataDeleted(removal.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(removal.OperationID, false); err != nil {
		t.Fatal(err)
	}
	originalSubmit := smtpSubmit
	t.Cleanup(func() { smtpSubmit = originalSubmit })
	var submitted smtpsubmit.Request
	smtpSubmit = func(_ context.Context, _ smtpsubmit.Config, request smtpsubmit.Request) error {
		submitted = request
		return nil
	}
	var stdout, stderr bytes.Buffer
	config := serverconfig.Config{
		Repositories: serverconfig.RepositoryFile{ResultsRoot: results},
		SMTPFrom:     "filees@example.test", MessageIDDomain: "filees.test",
		SMTP: smtpsubmit.Config{Address: "127.0.0.1:2525", ClientName: "filees.test", TLSMode: smtpsubmit.TLSNone},
	}
	if code := deliverPendingRealmRemovalMail(config, &stdout, &stderr); code != ExitOK {
		t.Fatalf("mail exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if submitted.Recipient != "erase@example.test" || !bytes.Contains(submitted.Message, []byte("has been completed")) {
		t.Fatalf("unexpected data-erasure message: %+v", submitted)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root, "outbox", removal.OperationID+".json"))
	if err != nil || bytes.Contains(raw, []byte("erase@example.test")) || !bytes.Contains(raw, []byte(`"delivery_state": "queued"`)) {
		t.Fatalf("data-erasure outbox was not scrubbed: %s err=%v", raw, err)
	}
}

func quote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
