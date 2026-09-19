package main

import (
	"filees/pkg/clientprofile"
	"filees/pkg/detachment"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
)

// The countdown's source is the file the demo server committed beside the
// view. Its absence - every ordinary server - and a damaged file both mean no
// countdown, never a failed projection.
func TestDemoExpiryIsReadBesideTheView(t *testing.T) {
	wc := t.TempDir()
	sync := clientview.SyncConfig{WorkingCopy: wc, RelativeViewPath: "view.json"}
	if got := demoExpiresAt(sync); got != "" {
		t.Fatalf("ordinary server announced %q", got)
	}
	announcement := `{"schema":"filees.client-demo/v1","expires_at":"2026-09-18T10:39:30.065536481Z","quota_bytes":1073741824}`
	if err := os.WriteFile(filepath.Join(wc, clientview.DemoFileName), []byte(announcement), 0o600); err != nil {
		t.Fatal(err)
	}
	// A later fact in the same file (quota_bytes here) must not hide this one.
	if got := demoExpiresAt(sync); got != "2026-09-18T10:39:30Z" {
		t.Fatalf("expires_at=%q", got)
	}
	if err := os.WriteFile(filepath.Join(wc, clientview.DemoFileName), []byte(`{"schema":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := demoExpiresAt(sync); got != "" {
		t.Fatalf("damaged announcement produced %q", got)
	}
	if got := demoExpiresAt(clientview.SyncConfig{}); got != "" {
		t.Fatalf("no working copy produced %q", got)
	}
}

func TestDemoRefusalCauseRequiresAnAnnouncedDeadline(t *testing.T) {
	wc := t.TempDir()
	profile := clientprofile.Profile{ServerID: "demo", ServiceWC: wc, RelativeViewPath: "view.json"}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	if got := refusedProfileCause(profile, now); got != detachment.CauseRevoked {
		t.Fatalf("server name alone inferred demo expiry: %s", got)
	}
	if err := os.WriteFile(filepath.Join(wc, clientview.DemoFileName), []byte(`{"schema":"filees.client-demo/v1","expires_at":"2026-09-19T12:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := refusedProfileCause(profile, now.Add(-time.Second)); got != detachment.CauseRevoked {
		t.Fatalf("early refusal labelled expiry: %s", got)
	}
	if got := refusedProfileCause(profile, now); got != detachment.CauseDemoExpired {
		t.Fatalf("refusal at the deadline: %s", got)
	}
}
