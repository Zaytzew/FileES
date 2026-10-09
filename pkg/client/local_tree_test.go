package client

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalTreeResultRequiresExactBinding(t *testing.T) {
	m := treeMoveFixture()
	raw := map[string]any{"state": "complete", "base_revision": float64(19), "revision": float64(20),
		"root_url": m.RootURL, "uuid": m.UUID, "source": m.Source, "destination": m.Destination}
	if got, err := parseLocalTreeResult(raw, "verify", m, 20); err != nil || got != "complete" {
		t.Fatal(got, err)
	}
	for key, bad := range map[string]any{"state": "inspect", "base_revision": float64(18), "revision": float64(21),
		"root_url": "file:///unrelated", "uuid": "other", "source": "other", "destination": "other"} {
		saved := raw[key]
		raw[key] = bad
		if _, err := parseLocalTreeResult(raw, "verify", m, 20); err == nil {
			t.Fatal("unrelated result accepted", key)
		}
		raw[key] = saved
	}
	if _, err := parseLocalTreeResult(raw, "inspect", m, 20); err == nil {
		t.Fatal("completion accepted as preflight")
	}
}

func TestLocalTreeInvalidScopeNeverReachesHelper(t *testing.T) {
	c := New(Options{NativeSVNPath: filepath.Join(t.TempDir(), "absent.exe")}).(LocalTreeReconciler)
	for _, test := range []struct {
		wc, phase string
		revision  int64
	}{
		{"relative", "inspect", 0}, {t.TempDir(), "cleanup", 20},
		{t.TempDir(), "inspect", 20}, {t.TempDir(), "ack", 19}, {t.TempDir(), "ack", 2147483648},
	} {
		if _, err := c.LocalTreeMetadata(t.Context(), test.wc, test.phase, treeMoveFixture(), test.revision); err == nil || !strings.Contains(err.Error(), "invalid WC") {
			t.Fatal("invalid scope reached helper", err)
		}
	}
}
