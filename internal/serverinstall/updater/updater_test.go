package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filees/internal/serverinstall/config"
	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/platform"
	"filees/internal/serverinstall/state"
)

type fakeOwnership struct {
	value platform.Ownership
}

type mapFetcher map[string][]byte

func (f mapFetcher) Cat(_ context.Context, path string) ([]byte, error) {
	data, ok := f[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (f fakeOwnership) Resolve(owner, group string) (platform.Ownership, error) {
	if owner == "" || group == "" {
		return platform.Ownership{}, errors.New("owner/group required")
	}
	return f.value, nil
}

func (f fakeOwnership) Stat(path string) (platform.Ownership, error)          { return f.value, nil }
func (f fakeOwnership) Apply(path string, ownership platform.Ownership) error { return nil }

func testRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Platform:     "openbsd-amd64",
		StateDir:     filepath.Join(root, "state"),
		StageDir:     filepath.Join(root, "stage"),
		BackupDir:    filepath.Join(root, "backups"),
		SbinDir:      filepath.Join(root, "sbin"),
		LibexecDir:   filepath.Join(root, "libexec"),
		SysconfDir:   filepath.Join(root, "etc/filees"),
		SSHDConfDir:  filepath.Join(root, "etc/ssh/sshd_config.d"),
		SSHKeysDir:   filepath.Join(root, "etc/ssh"),
		LoginConfDir: filepath.Join(root, "etc/login.conf.d"),
		DataDir:      filepath.Join(root, "var/filees"),
		OrphanFiles:  "keep",
	}
	return &Runner{Config: cfg, Ownership: fakeOwnership{value: platform.Ownership{UID: 100, GID: 200}}, Out: io.Discard}, root
}

func TestAdoptRecordsOnlyExactSignedBaseline(t *testing.T) {
	r, _ := testRunner(t)
	target := filepath.Join(r.Config.SbinDir, "filees-install")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("existing managed binary")
	if err := os.WriteFile(target, payload, 0o666); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	mode := formatMode(info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
	m := manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		ReleaseID:     "r1",
		Platform:      r.Config.Platform,
		Sequence:      1,
		SecurityEpoch: 1,
		Files: []manifest.File{{
			Source: "bin/filees-install", Target: "{sbin_dir}/filees-install", Kind: "binary",
			Mode: mode, Owner: "root", Group: "wheel", SHA256: sha256hex(payload),
		}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	r.Fetcher = mapFetcher{manifest.ReleaseManifestPath("r1", r.Config.Platform): raw}
	if err := r.Adopt(context.Background(), Options{ReleaseID: "r1"}); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(r.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.InstalledRelease != "r1" || st.HighestSequence != 1 || st.SecurityEpoch != 1 ||
		st.System == nil || !st.System.Adopted || len(st.History) != 0 {
		t.Fatalf("unexpected adopted state: %+v", st)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("adopt changed payload: %q, %v", got, err)
	}
	if err := r.Adopt(context.Background(), Options{ReleaseID: "r1"}); err == nil {
		t.Fatal("second adoption overwrote existing managed state")
	}
}

func TestInterruptedApplyRecoversAllPreimages(t *testing.T) {
	r, _ := testRunner(t)
	oldTarget := filepath.Join(r.Config.SbinDir, "old")
	newTarget := filepath.Join(r.Config.SbinDir, "new")
	stageOld := filepath.Join(r.Config.StageDir, "old")
	stageNew := filepath.Join(r.Config.StageDir, "new")
	for path, data := range map[string][]byte{
		oldTarget: []byte("before"), stageOld: []byte("after"), stageNew: []byte("new"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	ownership := platform.Ownership{UID: 100, GID: 200}
	staged := []StagedFile{
		{Target: oldTarget, StagePath: stageOld, Mode: 0o666, Ownership: ownership},
		{Target: newTarget, StagePath: stageNew, Mode: 0o666, Ownership: ownership},
	}
	entry, err := r.installStaged(staged, &state.State{}, "r2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.LoadTransaction(r.Config.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := r.recoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	oldData, err := os.ReadFile(oldTarget)
	if err != nil || string(oldData) != "before" {
		t.Fatalf("old target not restored: %q, %v", oldData, err)
	}
	if _, err := os.Stat(newTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new target survived recovery: %v", err)
	}
	if transaction, err := state.LoadTransaction(r.Config.StateDir); err != nil || transaction != nil {
		t.Fatalf("journal survived recovery: %+v, %v", transaction, err)
	}

	// A journal left after state.json committed is stale, not an instruction to
	// roll back the already successful update.
	if _, err := r.installStaged(staged, &state.State{}, "r2", nil); err != nil {
		t.Fatal(err)
	}
	committed := &state.State{InstalledRelease: "r2", History: []state.HistoryEntry{entry}}
	// Use the exact entry from the second transaction, whose timestamp can
	// differ from the first invocation.
	transaction, err := state.LoadTransaction(r.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	committed.History[0] = transaction.Entry
	if err := state.Save(r.Config.StateDir, committed); err != nil {
		t.Fatal(err)
	}
	if err := r.recoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	newData, err := os.ReadFile(newTarget)
	if err != nil || string(newData) != "new" {
		t.Fatalf("committed payload was rolled back: %q, %v", newData, err)
	}
}

func TestRecoveryPreflightsEveryBackupBeforeChangingTargets(t *testing.T) {
	r, _ := testRunner(t)
	ownership := platform.Ownership{UID: 100, GID: 200}
	var staged []StagedFile
	for _, name := range []string{"a", "b"} {
		target := filepath.Join(r.Config.SbinDir, name)
		stagePath := filepath.Join(r.Config.StageDir, name)
		for path, data := range map[string][]byte{target: []byte("old-" + name), stagePath: []byte("new-" + name)} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o666); err != nil {
				t.Fatal(err)
			}
		}
		staged = append(staged, StagedFile{Target: target, StagePath: stagePath, Mode: 0o666, Ownership: ownership})
	}
	entry, err := r.installStaged(staged, &state.State{}, "r3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.Files[1].BackupPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.recoverInterrupted(); err == nil {
		t.Fatal("recovery accepted a corrupt backup")
	}
	for _, name := range []string{"a", "b"} {
		got, err := os.ReadFile(filepath.Join(r.Config.SbinDir, name))
		if err != nil || string(got) != "new-"+name {
			t.Fatalf("target %s changed before preflight completed: %q, %v", name, got, err)
		}
	}
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
			{Source: "bin/same", Target: "{sbin_dir}/same", Owner: "root", Group: "wheel", Mode: "0755", SHA256: sha256hex(same)},
			{Source: "bin/drifted", Target: "{sbin_dir}/drifted", Owner: "root", Group: "wheel", SHA256: sha256hex([]byte("new"))},
			{Source: "bin/fresh", Target: "{sbin_dir}/fresh", Owner: "root", Group: "wheel", SHA256: sha256hex([]byte("x"))},
		},
	}
	plan, err := r.BuildPlan(m, &state.State{InstalledRelease: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.FirstInstall {
		t.Fatal("state without System must plan as first install")
	}
	sameAction := "UNCHANGED"
	if runtime.GOOS == "windows" {
		// Windows does not expose POSIX executable bits, so a server manifest's
		// 0755 policy is correctly reported as metadata drift in this host-side
		// unit test.
		sameAction = "METADATA"
	}
	want := map[string]string{"same": sameAction, "drifted": "UPDATE", "fresh": "ADD"}
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

func TestBuildPlanRejectsResolvedTargetOverlap(t *testing.T) {
	r, _ := testRunner(t)
	target := filepath.Join(r.Config.SbinDir, "same")
	m := &manifest.Manifest{ReleaseID: "r2", Platform: r.Config.Platform, Files: []manifest.File{
		{Source: "bin/a", Target: "{sbin_dir}/same", Owner: "root", Group: "wheel", SHA256: sha256hex([]byte("a"))},
		{Source: "bin/b", Target: target, Owner: "root", Group: "wheel", SHA256: sha256hex([]byte("b"))},
	}}
	if _, err := r.BuildPlan(m, &state.State{}); err == nil {
		t.Fatal("two manifest entries resolving to one target were accepted")
	}
	m.Files = m.Files[:1]
	m.Orphans = []manifest.Orphan{{Target: target}}
	if _, err := r.BuildPlan(m, &state.State{}); err == nil {
		t.Fatal("orphan overlapping a managed target was accepted")
	}
}

func TestResolveManifestRejectsRepositoryPathInjection(t *testing.T) {
	r, _ := testRunner(t)
	r.Fetcher = mapFetcher{}
	if _, err := r.ResolveManifest(context.Background(), "../r1"); err == nil {
		t.Fatal("release path traversal reached the fetcher")
	}
	r.Config.Channel = "../stable"
	if _, err := r.ResolveManifest(context.Background(), ""); err == nil {
		t.Fatal("channel path traversal reached the fetcher")
	}
	r.Config.Channel = "stable"
	r.Config.Platform = "../openbsd-amd64"
	if _, err := r.ResolveManifest(context.Background(), "r1"); err == nil {
		t.Fatal("platform path traversal reached the fetcher")
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
		{"4755", "binary", 0o755 | os.ModeSetuid},
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
