package client

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalFileDeletePreflightBoundAndTokenPrivate(t *testing.T) {
	in := remoteDeleteFixture()
	good := map[string]any{"mutation": "delete-file", "state": "present", "base_revision": float64(19), "local_base_revision": float64(17), "revision": float64(0), "root_url": in.RootURL, "uuid": in.UUID, "source": in.Source, "checksum_sha1": strings.Repeat("a", 40), "local_lock_token": "secret-lock"}
	p, err := parseLocalFileDeleteProof(good, in)
	if err != nil || p.BaseRevision != 17 || p.LockToken != "secret-lock" {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "secret-lock") {
		t.Fatal("token exposed")
	}
	for key, bad := range map[string]any{"local_base_revision": float64(20), "checksum_sha1": "bad", "local_lock_token": "bad\nsecret", "source": "another", "uuid": "other", "revision": float64(20), "state": "complete", "destination": "other"} {
		copy := map[string]any{}
		for k, v := range good {
			copy[k] = v
		}
		copy[key] = bad
		if _, err := parseLocalFileDeleteProof(copy, in); err == nil {
			t.Fatal("unbound preflight", key)
		}
	}
	good["state"] = "virtual"
	good["checksum_sha1"] = nil
	good["local_lock_token"] = nil
	if _, err := parseLocalFileDeleteProof(good, in); err != nil {
		t.Fatal(err)
	}
	delete(good, "local_lock_token")
	if _, err := parseLocalFileDeleteProof(good, in); err == nil {
		t.Fatal("missing token evidence treated as absence")
	}
}

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
