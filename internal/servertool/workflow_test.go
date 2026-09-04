package servertool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"filees/public-shares/channel"
)

func TestS1FilesystemWorkflow(t *testing.T) {
	if isolateSandboxingTest(t, "TestS1FilesystemWorkflow") {
		return
	}
	permitRepeatedSandbox(t)
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
  "schema":"filees.server-toolchain/v2",
  "display_name":"Serwer testowy",
  "root":` + quote(root) + `,
  "otp_pepper_file":` + quote(pepperPath) + `,
  "worker_public_key_file":` + quote(workerPublicPath) + `,
  "operation_ttl":"30m",
  "otp_attempts":3,
  "reverse_port_first":42000,
  "reverse_port_last":42010,
  "repositories":{"url_prefix":"svn+ssh://_filees-data@filees.test/"},
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

func TestUploadChannelInvitationMailUsesSharedSMTPWorker(t *testing.T) {
	results := filepath.Join(t.TempDir(), "results")
	if err := os.MkdirAll(filepath.Join(results, "realm-removals"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(results, "data-erasure"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	outbox := repoworker.UploadChannelOutbox{Root: filepath.Join(results, "public-shares", "upload-outbox"), Now: func() time.Time { return now }}
	record := channel.UploadRecord{ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "oferta-a"}
	if err := outbox.DeliverUploadTokens(context.Background(), record, []channel.Delivery{{Email: "a@example.test", Token: strings.Repeat("t", 43)}}); err != nil {
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
		SMTP:         smtpsubmit.Config{Address: "127.0.0.1:2525", ClientName: "filees.test", TLSMode: smtpsubmit.TLSNone},
		PublicShares: serverconfig.PublicSharesFile{BaseURL: "https://get.example.test"},
	}
	if code := deliverPendingRealmRemovalMail(config, &stdout, &stderr); code != ExitOK {
		t.Fatalf("mail exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if submitted.Recipient != "a@example.test" || !bytes.Contains(submitted.Message, []byte("/atmprojekt/oferta-a?invite=")) || !bytes.Contains(stdout.Bytes(), []byte("upload_channel_invitation")) {
		t.Fatalf("unexpected upload invitation: %+v stdout=%s", submitted, stdout.String())
	}
	if entries, err := os.ReadDir(outbox.Root); err != nil {
		t.Fatal(err)
	} else {
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".json" {
				t.Fatalf("sent job remained: %v", entries)
			}
		}
	}
}

func TestUploadChannelInvitationPermanentSMTPFailureStopsRetry(t *testing.T) {
	results := filepath.Join(t.TempDir(), "results")
	if err := os.MkdirAll(filepath.Join(results, "realm-removals"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(results, "data-erasure"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	outbox := repoworker.UploadChannelOutbox{Root: filepath.Join(results, "public-shares", "upload-outbox"), Now: func() time.Time { return now }}
	record := channel.UploadRecord{ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "oferta-a"}
	if err := outbox.DeliverUploadTokens(context.Background(), record, []channel.Delivery{{Email: "a@example.test", Token: strings.Repeat("t", 43)}}); err != nil {
		t.Fatal(err)
	}
	originalSubmit := smtpSubmit
	t.Cleanup(func() { smtpSubmit = originalSubmit })
	smtpSubmit = func(_ context.Context, _ smtpsubmit.Config, _ smtpsubmit.Request) error {
		return &smtpsubmit.Error{Stage: "rcpt_to", Code: 550, Err: errors.New("Invalid recipient")}
	}
	var stdout, stderr bytes.Buffer
	config := serverconfig.Config{
		Repositories: serverconfig.RepositoryFile{ResultsRoot: results},
		SMTPFrom:     "filees@example.test", MessageIDDomain: "filees.test",
		SMTP:         smtpsubmit.Config{Address: "127.0.0.1:2525", ClientName: "filees.test", TLSMode: smtpsubmit.TLSNone},
		PublicShares: serverconfig.PublicSharesFile{BaseURL: "https://get.example.test"},
	}
	if code := deliverPendingRealmRemovalMail(config, &stdout, &stderr); code != ExitUnavailable {
		t.Fatalf("mail exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	entries, err := os.ReadDir(outbox.Root)
	if err != nil {
		t.Fatal(err)
	}
	foundFailed := false
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(outbox.Root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"state": "failed"`) {
			t.Fatalf("job was not marked failed: %s", raw)
		}
		foundFailed = true
	}
	if !foundFailed {
		t.Fatal("rejected job missing")
	}
	stdout.Reset()
	stderr.Reset()
	if code := deliverPendingRealmRemovalMail(config, &stdout, &stderr); code != ExitOK {
		t.Fatalf("retry exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"no_work"`)) {
		t.Fatalf("rejected job was retried: %s", stdout.String())
	}
}

func quote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// A command that exits non-zero and prints nothing is the command-line form
// of "click it and nothing happens": the caller cannot tell a typo from a
// stale binary. That is not hypothetical - it cost a live debugging session on
// 2026-08-06, where `ticket create` with flags before the positional e-mail
// exited mute and looked like the feature was missing.
//
// Every usage refusal is walked here rather than the one that was noticed,
// because the defect is a class, not an instance.
func TestAdminUsageRefusalsAlwaysExplainThemselves(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"ticket"},
		{"ticket", "resend"},
		{"ticket", "revoke"},
		{"ticket", "list", "extra"},
		{"operation", "inspect"},
		{"client", "revoke"},
		{"client", "revoke-realm"},
		{"repo", "transfer-owner"},
		{"repo", "transfer-owner", "-repo-id", "only-one-of-two"},
		{"repo", "check-state", "extra"},
		{"repo", "prune", "extra"},
		{"erasure", "complete"},
		{"nonsense", "command"},
	} {
		var stdout, stderr strings.Builder
		code := RunAdmin(args, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("RunAdmin(%q) = %d, want ExitUsage", args, code)
		}
		if strings.TrimSpace(stderr.String()) == "" {
			t.Fatalf("RunAdmin(%q) refused silently; a caller cannot tell a typo from a broken binary", args)
		}
	}
}

// Without a version there is no way to tell how old a deployed binary is
// except by comparing its usage text to the source, which is exactly how a
// stale filees-admin went unnoticed while a newly added flag read as "not
// defined". It must answer before any config is loaded, because a suspect
// config is a common reason to be asking.
func TestAdminReportsVersionWithoutNeedingAConfig(t *testing.T) {
	for _, spelling := range []string{"version", "--version", "-version"} {
		var stdout, stderr strings.Builder
		if code := RunAdmin([]string{spelling}, &stdout, &stderr); code != ExitOK {
			t.Fatalf("RunAdmin(%q) = %d, want ExitOK", spelling, code)
		}
		if strings.TrimSpace(stdout.String()) == "" {
			t.Fatalf("RunAdmin(%q) printed no version", spelling)
		}
	}
}
