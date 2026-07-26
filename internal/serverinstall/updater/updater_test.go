package updater

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/serverinstall/config"
	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
)

func testRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		SbinDir:      filepath.Join(root, "sbin"),
		LibexecDir:   filepath.Join(root, "libexec"),
		SysconfDir:   filepath.Join(root, "etc/filees"),
		SSHDConfDir:  filepath.Join(root, "etc/ssh/sshd_config.d"),
		SSHKeysDir:   filepath.Join(root, "etc/ssh"),
		LoginConfDir: filepath.Join(root, "etc/login.conf.d"),
		DataDir:      filepath.Join(root, "var/filees"),
		OrphanFiles:  "keep",
	}
	return &Runner{Config: cfg, Out: io.Discard}, root
}

func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestBuildPlanActions(t *testing.T) {
	r, _ := testRunner(t)
	sbin := r.Config.SbinDir
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	same := []byte("unchanged content")
	if err := os.WriteFile(filepath.Join(sbin, "same"), same, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbin, "drifted"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{
		ReleaseID: "r2",
		Platform:  "openbsd-amd64",
		Files: []manifest.File{
			{Source: "bin/same", Target: "{sbin_dir}/same", SHA256: sha256hex(same)},
			{Source: "bin/drifted", Target: "{sbin_dir}/drifted", SHA256: sha256hex([]byte("new"))},
			{Source: "bin/fresh", Target: "{sbin_dir}/fresh", SHA256: sha256hex([]byte("x"))},
		},
	}
	plan, err := r.BuildPlan(m, &state.State{InstalledRelease: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.FirstInstall {
		t.Fatal("state without System must plan as first install")
	}
	want := map[string]string{"same": "UNCHANGED", "drifted": "UPDATE", "fresh": "ADD"}
	for _, f := range plan.Files {
		base := filepath.Base(f.Target)
		if f.Action != want[base] {
			t.Errorf("%s: action %s, want %s", base, f.Action, want[base])
		}
	}
	if len(plan.Files) != 3 {
		t.Fatalf("plan files = %d", len(plan.Files))
	}
}

func TestCheckConfigContract(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "server.conf")
	content := "[core]\nrealm = a\nold_key = legacy\ntimeout = 30\n"
	if err := os.WriteFile(confPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	issues := checkConfigContract(manifest.ConfigContract{
		Name:            "server",
		Path:            confPath,
		RequiredKeys:    []string{"core.realm", "core.missing_required"},
		RecommendedKeys: []string{"core.missing_recommended"},
		DeprecatedKeys:  []string{"core.old_key"},
		DefaultChanged:  []manifest.DefaultChange{{Key: "core.timeout", Old: "30", New: "60"}},
	})
	bySeverity := map[string]int{}
	for _, issue := range issues {
		bySeverity[issue.Severity]++
	}
	if bySeverity["FAIL"] != 1 {
		t.Errorf("FAIL count = %d, want 1 (missing required only)", bySeverity["FAIL"])
	}
	if bySeverity["WARN"] != 3 {
		t.Errorf("WARN count = %d, want 3 (recommended, deprecated, default change)", bySeverity["WARN"])
	}

	unreadable := checkConfigContract(manifest.ConfigContract{Name: "gone", Path: filepath.Join(dir, "nope")})
	if len(unreadable) != 1 || unreadable[0].Severity != "FAIL" {
		t.Fatalf("unreadable config: %+v", unreadable)
	}
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		raw, kind string
		want      os.FileMode
	}{
		{"", "binary", 0o755},
		{"", "config", 0o644},
		{"4755", "binary", 0o4755},
		{"600", "", 0o600},
	}
	for _, c := range cases {
		got, err := parseMode(c.raw, c.kind)
		if err != nil {
			t.Fatalf("parseMode(%q,%q): %v", c.raw, c.kind, err)
		}
		if got != c.want {
			t.Errorf("parseMode(%q,%q) = %o, want %o", c.raw, c.kind, got, c.want)
		}
	}
	if _, err := parseMode("rwx", ""); err == nil {
		t.Error("non-octal mode must fail")
	}
}

func TestCopyFilePreservesSetuid(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "suidbin")
	if err := os.WriteFile(src, []byte("#!x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, os.FileMode(0o755)|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSetuid == 0 {
		t.Skip("filesystem does not support setuid here")
	}

	dst := filepath.Join(dir, "copy")
	if err := copyFile(src, dst, 0); err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode()&os.ModeSetuid == 0 {
		t.Fatal("setuid bit lost in backup/restore copy")
	}
}

func TestManifestTouchesSSHD(t *testing.T) {
	r, _ := testRunner(t)
	plain := &manifest.Manifest{Files: []manifest.File{
		{Source: "a", Target: "{sbin_dir}/a"},
	}}
	if r.manifestTouchesSSHD(plain) {
		t.Fatal("sbin-only manifest must not classify as system run")
	}
	withFrag := &manifest.Manifest{Files: []manifest.File{
		{Source: "a", Target: "{sbin_dir}/a"},
		{Source: "f", Target: "{sshd_conf_dir}/filees.conf"},
	}}
	if !r.manifestTouchesSSHD(withFrag) {
		t.Fatal("sshd fragment manifest must classify as system run")
	}
}

func TestSSHDFragmentUpdated(t *testing.T) {
	r, _ := testRunner(t)
	frag := filepath.Join(r.Config.SSHDConfDir, "filees.conf")
	for action, want := range map[string]bool{"UPDATE": true, "ADD": true, "UNCHANGED": false} {
		plan := &Plan{Files: []FilePlan{{Target: frag, Action: action}}}
		if got := r.sshdFragmentUpdated(plan); got != want {
			t.Errorf("action %s: got %v, want %v", action, got, want)
		}
	}
}

// TestCheckFreshnessGatesRollback covers the updater's half of the audit's
// Finding E: a stale release must be refused before anything is staged, and a
// deliberate downgrade must require an explicit operator override rather than
// arriving disguised as an ordinary update.
func TestCheckFreshnessGatesRollback(t *testing.T) {
	r, _ := testRunner(t)
	installed := &state.State{InstalledRelease: "v11", HighestSequence: 11, SecurityEpoch: 3}
	stale := &manifest.Manifest{ReleaseID: "v10", Sequence: 10, SecurityEpoch: 3}
	fresh := &manifest.Manifest{ReleaseID: "v12", Sequence: 12, SecurityEpoch: 3}

	if err := r.checkFreshness(fresh, installed, Options{}); err != nil {
		t.Fatalf("a newer release was refused: %v", err)
	}
	err := r.checkFreshness(stale, installed, Options{})
	if err == nil {
		t.Fatal("a stale release passed the freshness gate")
	}
	if !errors.Is(err, state.ErrRollback) {
		t.Fatalf("refusal is not ErrRollback: %v", err)
	}
	// The override is deliberate, explicit, and announced.
	var out bytes.Buffer
	r.Out = &out
	if err := r.checkFreshness(stale, installed, Options{AllowRollback: true}); err != nil {
		t.Fatalf("explicit rollback override refused: %v", err)
	}
	if !strings.Contains(out.String(), "rollback explicitly allowed") {
		t.Fatalf("rollback override was silent: %q", out.String())
	}
	// The override must not turn into a blanket bypass of unrelated failures.
	uncounted := &manifest.Manifest{ReleaseID: "v13", Sequence: 0, SecurityEpoch: 0}
	if err := r.checkFreshness(uncounted, installed, Options{}); err == nil {
		t.Fatal("a release without freshness counters passed the gate")
	}
}
