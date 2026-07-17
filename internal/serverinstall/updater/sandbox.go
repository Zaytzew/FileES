package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
)

type unveilSpec struct {
	Label string
	Path  string
	Perms string
}

// baseUnveils returns paths every action needs: config file, state/stage/backup
// dirs, /dev/null (exec defaults unset stdio to it), and the svn binary.
// Must be called before any unveil() — resolves binary paths via $PATH first.
func (r *Runner) baseUnveils() ([]unveilSpec, error) {
	if !sandboxEnabled() {
		return nil, nil
	}
	cfg := r.Config
	svn, err := resolveProgramPath(strings.TrimSpace(cfg.SVNPath))
	if err != nil {
		return nil, fmt.Errorf("resolve svn %q: %w", cfg.SVNPath, err)
	}
	specs := []unveilSpec{
		{Label: "cfg", Path: cfg.ConfigPath, Perms: "r"},
		{Label: "state", Path: cfg.StateDir, Perms: "rwc"},
		{Label: "stage", Path: cfg.StageDir, Perms: "rwc"},
		{Label: "backup", Path: cfg.BackupDir, Perms: "rwc"},
		{Label: "null", Path: "/dev/null", Perms: "rw"},
		{Label: "svn", Path: svn, Perms: "rx"},
	}
	return specs, nil
}

// manifestUnveils adds the directories each manifest file target and config
// contract path lives in. write=true adds rwc for install; write=false (check/
// dry-run) uses r. Config contracts are always r regardless.
// Uses directory-level granularity only — never per-file — because
// tempInstallPath creates a sibling temp file whose name is not known until
// install time, and unveil(2) requires the path to exist.
func (r *Runner) manifestUnveils(m *manifest.Manifest, write bool) []unveilSpec {
	perm := "r"
	if write {
		perm = "rwc"
	}
	b := newUnveilBuilder()
	dirs := r.dirs()
	for _, mf := range m.Files {
		b.add("target_dir", filepath.Dir(manifest.ResolveTarget(dirs, mf.Target)), perm)
	}
	for _, cc := range m.Configs {
		if strings.TrimSpace(cc.Path) == "" {
			continue
		}
		b.add("config_dir", filepath.Dir(cc.Path), "r")
	}
	if write && r.Config.OrphanFiles == "remove" {
		for _, o := range m.Orphans {
			b.add("orphan_dir", filepath.Dir(manifest.ResolveOrphanTarget(dirs, o)), perm)
		}
	}
	return b.specs()
}

// rollbackUnveils returns paths a rollback needs from the last history entry.
func rollbackUnveils(entry state.HistoryEntry) []unveilSpec {
	b := newUnveilBuilder()
	for _, bf := range entry.Files {
		b.add("target_dir", filepath.Dir(bf.Target), "rwc")
		if bf.BackupPath != "" {
			b.add("backup_dir", filepath.Dir(bf.BackupPath), "r")
		}
	}
	return b.specs()
}

// System runs (first-install, purge, and upgrades touching the sshd
// fragment) do NOT unveil at all: unveil(2) state is inherited across
// fork/exec, so the useradd/userdel/cap_mkdb/sshd/rcctl children these runs
// spawn would see an empty filesystem — /etc/passwd, /etc/spwd.db,
// /etc/ssh and the rc machinery are not (and must not become) part of the
// unveil profile. Unveiling /etc wide enough for them would keep the
// mechanism's letter while voiding its point. Those runs instead keep the
// bootstrap pledge (with proc exec) until their system tasks finish, then
// drop to file promises via reducePledge. File-only runs (check, dry-run,
// upgrade, rollback) get the full two-pass unveil in applyUnveils.

// --- unveilBuilder ---

type unveilBuilder struct {
	order []string
	byDir map[string]*unveilSpec
}

func newUnveilBuilder() *unveilBuilder {
	return &unveilBuilder{byDir: map[string]*unveilSpec{}}
}

func (b *unveilBuilder) add(label, dir, perms string) {
	if existing, ok := b.byDir[dir]; ok {
		if perms == "rwc" {
			existing.Perms = "rwc"
		}
		if !strings.Contains(existing.Label, label) {
			existing.Label += "+" + label
		}
		return
	}
	spec := unveilSpec{Label: label, Path: dir, Perms: perms}
	b.byDir[dir] = &spec
	b.order = append(b.order, dir)
}

func (b *unveilBuilder) specs() []unveilSpec {
	out := make([]unveilSpec, 0, len(b.order))
	for _, dir := range b.order {
		out = append(out, *b.byDir[dir])
	}
	return out
}

// resolveAndMergeSpecs resolves each path to its nearest existing ancestor
// then re-merges. Two distinct logical directories can collapse to the same
// ancestor after resolution; the second merge prevents duplicate Unveil() calls.
func resolveAndMergeSpecs(specs []unveilSpec) []unveilSpec {
	b := newUnveilBuilder()
	for _, s := range specs {
		b.add(s.Label, nearestExistingPath(s.Path), s.Perms)
	}
	return b.specs()
}

// nearestExistingPath returns path if it exists, else its nearest existing
// ancestor. unveil(2) requires the path to already exist.
func nearestExistingPath(path string) string {
	current := path
	for {
		if _, err := os.Stat(current); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}

func resolveProgramPath(program string) (string, error) {
	if filepath.IsAbs(program) {
		return filepath.Clean(program), nil
	}
	return exec.LookPath(program)
}
