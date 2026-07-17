package updater

import (
	"os"
	"path/filepath"
	"testing"

	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
)

func TestUnveilBuilderMergesSameDir(t *testing.T) {
	b := newUnveilBuilder()
	b.add("target_dir", "/usr/local/sbin", "r")
	b.add("config_dir", "/usr/local/sbin", "rwc")
	b.add("other", "/etc/filees", "r")
	specs := b.specs()
	if len(specs) != 2 {
		t.Fatalf("specs = %d, want 2 (same dir merged)", len(specs))
	}
	if specs[0].Path != "/usr/local/sbin" || specs[0].Perms != "rwc" {
		t.Fatalf("merged spec must widen to rwc: %+v", specs[0])
	}
	if specs[1].Path != "/etc/filees" {
		t.Fatalf("insertion order lost: %+v", specs)
	}
}

func TestUnveilBuilderNeverNarrows(t *testing.T) {
	b := newUnveilBuilder()
	b.add("a", "/x", "rwc")
	b.add("b", "/x", "r")
	if got := b.specs()[0].Perms; got != "rwc" {
		t.Fatalf("later r request must not narrow rwc: %q", got)
	}
}

func TestNearestExistingPath(t *testing.T) {
	dir := t.TempDir()
	if got := nearestExistingPath(dir); got != dir {
		t.Fatalf("existing path must return itself: %q", got)
	}
	missing := filepath.Join(dir, "a/b/c")
	if got := nearestExistingPath(missing); got != dir {
		t.Fatalf("missing path must fall back to nearest ancestor %q, got %q", dir, got)
	}
	if got := nearestExistingPath("/nonexistent-filees-root-xyz"); got != "/" {
		t.Fatalf("fully missing path must resolve to /: %q", got)
	}
}

// The historical ksefUCK regression: two logically distinct directories that
// both resolve to the same existing ancestor must merge into ONE unveil call
// after resolution, at the widest permission.
func TestResolveAndMergeSpecsCollapsesToAncestor(t *testing.T) {
	dir := t.TempDir()
	specs := resolveAndMergeSpecs([]unveilSpec{
		{Label: "a", Path: filepath.Join(dir, "missing-one"), Perms: "r"},
		{Label: "b", Path: filepath.Join(dir, "missing-two"), Perms: "rwc"},
	})
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1 (both collapse to %s)", len(specs), dir)
	}
	if specs[0].Path != dir || specs[0].Perms != "rwc" {
		t.Fatalf("collapsed spec: %+v", specs[0])
	}
}

func TestManifestUnveilsDirGranularityAndPerms(t *testing.T) {
	r, root := testRunner(t)
	m := &manifest.Manifest{Files: []manifest.File{
		{Source: "a", Target: "{sbin_dir}/filees-entry"},
		{Source: "b", Target: "{sbin_dir}/filees-onboard"},
	}, Configs: []manifest.ConfigContract{
		{Name: "server", Path: filepath.Join(root, "etc/filees/server.json")},
	}}

	read := r.manifestUnveils(m, false)
	if len(read) != 2 {
		t.Fatalf("read specs = %d, want 2 (sbin merged + config dir)", len(read))
	}
	for _, s := range read {
		if s.Path == r.Config.SbinDir && s.Perms != "r" {
			t.Fatalf("check mode must be read-only: %+v", s)
		}
		if filepath.Base(s.Path) == "filees-entry" {
			t.Fatal("unveil must be directory-level, never per-file")
		}
	}

	write := r.manifestUnveils(m, true)
	for _, s := range write {
		if s.Path == r.Config.SbinDir && s.Perms != "rwc" {
			t.Fatalf("apply mode must be rwc on target dir: %+v", s)
		}
		if s.Path == filepath.Join(root, "etc/filees") && s.Perms != "r" {
			t.Fatalf("config contract dir must stay read-only even on apply: %+v", s)
		}
	}
}

func TestRollbackUnveils(t *testing.T) {
	entry := state.HistoryEntry{Files: []state.BackupFile{
		{Target: "/usr/local/sbin/filees-entry", BackupPath: "/var/b/usr/local/sbin/filees-entry", Existed: true},
		{Target: "/usr/local/sbin/filees-onboard"},
	}}
	specs := rollbackUnveils(entry)
	byPath := map[string]string{}
	for _, s := range specs {
		byPath[s.Path] = s.Perms
	}
	if byPath["/usr/local/sbin"] != "rwc" {
		t.Fatalf("target dir perms: %q", byPath["/usr/local/sbin"])
	}
	if byPath["/var/b/usr/local/sbin"] != "r" {
		t.Fatalf("backup dir must be read-only: %q", byPath["/var/b/usr/local/sbin"])
	}
}

func TestTempInstallPathIsSiblingAndDeterministic(t *testing.T) {
	p1 := tempInstallPath("/usr/local/sbin/filees-entry")
	p2 := tempInstallPath("/usr/local/sbin/filees-entry")
	if p1 != p2 {
		t.Fatal("temp path must be deterministic for unveil planning")
	}
	if filepath.Dir(p1) != "/usr/local/sbin" {
		t.Fatalf("temp path must stay in target dir: %q", p1)
	}
	if !os.IsPathSeparator(p1[0]) {
		t.Fatalf("temp path must be absolute: %q", p1)
	}
}
