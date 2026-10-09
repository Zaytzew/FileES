package client

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalFileDeleteResultBoundToOneFile(t *testing.T) {
	in := remoteDeleteFixture()
	raw := map[string]any{"mutation": "delete-file", "state": "complete", "base_revision": float64(19),
		"revision": float64(20), "root_url": in.RootURL, "uuid": in.UUID, "source": in.Source}
	for _, phase := range []string{"stage", "ack", "verify"} {
		if got, err := parseLocalFileDeleteResult(raw, phase, in, 20); err != nil || got != "complete" {
			t.Fatal(got, err)
		}
	}
	if _, err := parseLocalFileDeleteResult(raw, "erase", in, 20); err == nil {
		t.Fatal("unknown phase accepted")
	}
	for key, bad := range map[string]any{"mutation": "move", "state": "present", "base_revision": float64(18),
		"revision": float64(21), "root_url": "file:///foreign", "uuid": "foreign", "source": "another.txt"} {
		saved := raw[key]
		raw[key] = bad
		if _, err := parseLocalFileDeleteResult(raw, "verify", in, 20); err == nil {
			t.Fatal("foreign result accepted", key)
		}
		raw[key] = saved
	}
	raw["destination"] = ""
	if _, err := parseLocalFileDeleteResult(raw, "verify", in, 20); err == nil {
		t.Fatal("move-shaped result accepted")
	}
	delete(raw, "destination")
	raw["revision"] = float64(0)
	for _, state := range []string{"present", "virtual"} {
		raw["state"] = state
		if got, err := parseLocalFileDeleteResult(raw, "inspect", in, 0); err != nil || got != state {
			t.Fatal(got, err)
		}
	}
	if _, err := parseLocalFileDeleteResult(raw, "ack", in, 0); err == nil {
		t.Fatal("preflight used as ACK")
	}
}

func TestLocalFileDeleteInvalidScopeNeverExecutes(t *testing.T) {
	c := New(Options{NativeSVNPath: filepath.Join(t.TempDir(), "absent.exe")}).(LocalFileDeleteReconciler)
	for _, test := range []struct {
		wc, phase string
		revision  int64
	}{
		{"relative", "inspect", 0}, {t.TempDir(), "prepare", 20}, {t.TempDir(), "cleanup", 20},
		{t.TempDir(), "inspect", 20}, {t.TempDir(), "ack", 19}, {t.TempDir(), "ack", math.MaxInt32 + 1},
		{t.TempDir(), "verify", -1},
	} {
		if _, err := c.LocalFileDeleteMetadata(t.Context(), test.wc, test.phase, remoteDeleteFixture(), test.revision); err == nil || !strings.Contains(err.Error(), "invalid WC") {
			t.Fatal("invalid scope reached helper", err)
		}
	}
}
