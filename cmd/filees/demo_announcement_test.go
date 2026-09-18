package main

import (
	"os"
	"path/filepath"
	"testing"

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
