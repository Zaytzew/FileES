package releasepublish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	installmanifest "filees/internal/serverinstall/manifest"
)

func TestGenerateIsDeterministicSortedAndConsumable(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "bin/z", "z")
	writePayload(t, root, "bin/a", "a")
	spec := Spec{ReleaseID: "r178", Platform: "openbsd-amd64", SVNRevision: "178", Sequence: 178, SecurityEpoch: 1, Files: []FileSpec{
		{Source: "bin/z", Target: "{libexec_dir}/filees/z", Kind: "executable", Mode: "755", Owner: "root", Group: "wheel"},
		{Source: "bin/a", Target: "{sbin_dir}/a", Kind: "executable", Mode: "0755", Owner: "root", Group: "wheel"},
	}}
	first, err := Generate(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("identical input produced different manifests")
	}
	parsed, err := installmanifest.Parse(first)
	if err != nil {
		t.Fatalf("generated manifest is rejected by installer: %v", err)
	}
	if parsed.Files[0].Source != "bin/a" || parsed.Files[1].Source != "bin/z" {
		t.Fatalf("files are not sorted: %+v", parsed.Files)
	}
	if parsed.Files[0].Mode != "0755" || parsed.Files[1].Mode != "0755" {
		t.Fatalf("modes are not canonical: %+v", parsed.Files)
	}
	if parsed.CreatedAt != "" {
		t.Fatal("deterministic manifest unexpectedly contains a generated timestamp")
	}
}

func TestGenerateRejectsDuplicatesTraversalAndEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "bin/a", "a")
	base := Spec{ReleaseID: "r1", Platform: "linux-amd64", Sequence: 1, SecurityEpoch: 1}
	bad := []Spec{
		{ReleaseID: base.ReleaseID, Platform: base.Platform, Sequence: base.Sequence, SecurityEpoch: base.SecurityEpoch, Files: []FileSpec{{Source: "../a", Target: "/a", Owner: "root", Group: "wheel"}}},
		{ReleaseID: base.ReleaseID, Platform: base.Platform, Sequence: base.Sequence, SecurityEpoch: base.SecurityEpoch, Files: []FileSpec{{Source: "bin/a", Target: "/a", Owner: "root", Group: "wheel"}, {Source: "bin/a2", Target: "/a", Owner: "root", Group: "wheel"}}},
	}
	for _, spec := range bad {
		if _, err := Generate(root, spec); err == nil {
			t.Fatalf("accepted invalid spec: %+v", spec)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink creation unavailable on this test host: %v", err)
	}
	base.Files = []FileSpec{{Source: "escape", Target: "/secret", Owner: "root", Group: "wheel"}}
	if _, err := Generate(root, base); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("escaping symlink error = %v", err)
	}
}

func TestGenerateAllowsOnePayloadAtDifferentPrivilegeBoundaries(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "bin/recovery", "same executable")
	spec := Spec{ReleaseID: "r1", Platform: "openbsd-amd64", Sequence: 1, SecurityEpoch: 1, Files: []FileSpec{
		{Source: "bin/recovery", Target: "{libexec_dir}/filees/recovery-entry", Mode: "4550", Owner: "_filees-state", Group: "_filees-recovery"},
		{Source: "bin/recovery", Target: "{libexec_dir}/filees/recovery-authorize", Mode: "0555", Owner: "root", Group: "wheel"},
	}}
	raw, err := Generate(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := installmanifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Files) != 2 || parsed.Files[0].Target == parsed.Files[1].Target {
		t.Fatalf("shared payload targets not retained: %+v", parsed.Files)
	}
}

func TestLoadSpecIsStrictAndWriteAtomic(t *testing.T) {
	root := t.TempDir()
	specPath := filepath.Join(root, "spec.json")
	if err := os.WriteFile(specPath, []byte(`{"release_id":"r1","platform":"openbsd-amd64","files":[],"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(specPath); err == nil {
		t.Fatal("accepted unknown spec field")
	}
	out := filepath.Join(root, "release", "manifest.json")
	raw := []byte("manifest\n")
	if err := WriteAtomic(out, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("written manifest = %q, %v", got, err)
	}
}

func writePayload(t *testing.T, root, relative, value string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateRequiresFreshnessCounters keeps the release generator in step
// with the installer's anti-rollback contract (audit Finding E): a release that
// carries no position in the ordering cannot be protected against replay, so it
// must not be publishable in the first place.
func TestGenerateRequiresFreshnessCounters(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []FileSpec{{Source: "a", Target: "/a", Owner: "root", Group: "wheel"}}
	for _, spec := range []Spec{
		{ReleaseID: "r1", Platform: "linux-amd64", Files: files},
		{ReleaseID: "r1", Platform: "linux-amd64", Sequence: 1, Files: files},
		{ReleaseID: "r1", Platform: "linux-amd64", SecurityEpoch: 1, Files: files},
	} {
		if _, err := Generate(root, spec); err == nil {
			t.Fatalf("generated a manifest without freshness counters: %+v", spec)
		}
	}
	if _, err := Generate(root, Spec{ReleaseID: "r1", Platform: "linux-amd64", Sequence: 1, SecurityEpoch: 1, Files: files}); err != nil {
		t.Fatalf("fully counted spec rejected: %v", err)
	}
}
