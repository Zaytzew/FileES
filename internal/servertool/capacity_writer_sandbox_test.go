//go:build !windows

package servertool

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

// Exercise the complete guard -> real action -> locked unveil -> acknowledgement
// sequence, not merely the inner action. A marker left after success would stop
// later capacity recovery even though the onboarding receipt was already saved.
func TestOnboardAcknowledgesCapacityWriterUnderSandbox(t *testing.T) {
	if isolateSandboxingTest(t, "TestOnboardAcknowledgesCapacityWriterUnderSandbox") {
		return
	}
	path, _, _ := writeRepoPruneFixtureConfig(t)
	c, err := serverconfig.LoadFor(path, serverconfig.SecretOTP)
	if err != nil {
		t.Fatal(err)
	}
	files, err := onboarding.OpenExisting(c.Root, c.Onboarding, onboarding.Access{Areas: onboarding.AreaAll, NeedOTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.CreateTicket("writer@example.test", onboarding.Policy{RealmID: uuid.NewString()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(onboarding.OnboardRequest{Schema: onboarding.LegacyOnboardRequestSchema, Email: "writer@example.test", OnboardingRequestID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := RunOnboard([]string{"-config", path, "take"}, bytes.NewReader(request), &out, &diagnostic); code != ExitOK {
		t.Fatalf("onboard exit=%d diagnostic=%s", code, diagnostic.String())
	}
	entries, err := os.ReadDir(filepath.Join(c.Activation.Root, "capacity-active-writers"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("unacknowledged capacity writer: entries=%v err=%v", entries, err)
	}
	outbox, err := files.ListOutbox()
	if err != nil || len(outbox) != 1 {
		t.Fatalf("onboarding effect missing: entries=%d err=%v", len(outbox), err)
	}
	if runtime.GOOS == "openbsd" {
		// Completing the guard must not give onboard the activation authority.
		if _, err := os.Stat(c.Activation.AuthorizedKeysFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("activation authority unexpectedly visible: %v", err)
		}
	}
}
