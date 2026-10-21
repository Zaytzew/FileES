package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
	"filees/internal/storagewatch"
)

// No positive archive-upgrade protocol has been accepted yet. In particular a
// supported hook binary alone says nothing about resident services or rollback
// pre-images. Never add an allow-rollback exception to this refusal.
var ErrArchiveMaintenance = errors.New("archive protection requires a coordinated server upgrade; this installer cannot replace or restore this installation")

type archiveInstallPaths struct {
	Activation struct {
		Root      string `json:"root"`
		ServiceWC string `json:"service_working_copy"`
	} `json:"activation"`
	Repositories struct {
		Root string `json:"root"`
	} `json:"repositories"`
}

// Even Check may restore a transaction. All public actions enter before that
// recovery, not just before stageFiles/installStaged.
func (r *Runner) Check(ctx context.Context, o Options) error {
	if err := r.withArchiveInstallAdmission(ctx, func() error { return nil }); err != nil {
		return err
	}
	return r.check(ctx, o)
}
func (r *Runner) Adopt(ctx context.Context, o Options) error {
	if err := r.withArchiveInstallAdmission(ctx, func() error { return nil }); err != nil {
		return err
	}
	return r.adopt(ctx, o)
}
func (r *Runner) Apply(ctx context.Context, o Options) error {
	if err := r.withArchiveInstallAdmission(ctx, func() error { return nil }); err != nil {
		return err
	}
	return r.apply(ctx, o)
}
func (r *Runner) Rollback() error {
	return r.withArchiveInstallAdmission(context.Background(), r.rollback)
}
func (r *Runner) Purge(o PurgeOptions) error {
	return r.withArchiveInstallAdmission(context.Background(), func() error { return r.purge(o) })
}

func (r *Runner) recoverInterrupted() error {
	return r.withArchiveInstallAdmission(context.Background(), r.recoverInterruptedUnderArchiveLease)
}

func (r *Runner) installStaged(files []StagedFile, st *state.State, release string, orphans []manifest.Orphan) (state.HistoryEntry, error) {
	var entry state.HistoryEntry
	err := r.withArchiveInstallAdmission(context.Background(), func() error {
		for _, file := range files {
			if err := r.checkArchiveConfigurationChange(file.Target, file.StagePath); err != nil {
				return err
			}
		}
		var err error
		entry, err = r.installStagedUnderArchiveLease(files, st, release, orphans)
		return err
	})
	return entry, err
}

// A configuration pre-image must not switch the roots underneath the held
// lease. Moving archive authority needs its own coordinated migration, not an
// incidental install/rollback. Unrelated configuration fields may still change.
func (r *Runner) checkArchiveConfigurationChange(target, candidate string) error {
	config := filepath.Join(r.Config.SysconfDir, "server.json")
	if filepath.Clean(target) != filepath.Clean(config) {
		return nil
	}
	var current, next archiveInstallPaths
	err := readArchiveInstallJSON(config, &current)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := readArchiveInstallJSON(candidate, &next); err != nil {
		return err
	}
	if err == nil && current != next {
		return errors.New("installer cannot change archive authority roots; a coordinated migration is required")
	}
	for _, root := range []string{next.Activation.Root, next.Activation.ServiceWC, next.Repositories.Root} {
		if root != "" && (!filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator)) {
			return errors.New("archive install paths must be absolute dedicated directories")
		}
	}
	return next.check()
}

func (r *Runner) checkArchiveRestoreConfiguration(entry state.HistoryEntry) error {
	for _, file := range entry.Files {
		if file.Existed {
			if err := r.checkArchiveConfigurationChange(file.Target, file.BackupPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) withArchiveInstallAdmission(ctx context.Context, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var paths archiveInstallPaths
	config := filepath.Join(r.Config.SysconfDir, "server.json")
	if err := readArchiveInstallJSON(config, &paths); errors.Is(err, os.ErrNotExist) {
		return run() // bootstrap: no configured archive executor
	} else if err != nil {
		return fmt.Errorf("archive install configuration: %w", err)
	}
	for _, root := range []string{paths.Activation.Root, paths.Activation.ServiceWC, paths.Repositories.Root} {
		if root != "" && (!filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator)) {
			return errors.New("archive install paths must be absolute dedicated directories")
		}
	}
	inspect := func() error {
		if err := paths.check(); err != nil {
			return err
		}
		return run()
	}
	if paths.Activation.Root == "" {
		// A legacy configuration cannot start the new archive executor. Still
		// refuse orphan canonical/runtime state instead of assuming it is ordinary.
		return inspect()
	}
	if _, err := archiveInstallDirectory(paths.Activation.Root); err != nil {
		return fmt.Errorf("archive install writer root: %w", err)
	}
	// Same inode/exclusive order as freeze/release. Cover each complete file
	// cutover/recovery (all of rollback/purge), but NOT network payload staging.
	// The cutover rechecks state in case freeze won during that download. Do not
	// signal sessions, open a held gate or restart residents to install files.
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return (storagewatch.Gate{Root: paths.Activation.Root}).WithExclusive(wait, inspect)
}

func archiveInstallDirectory(path string) ([]os.DirEntry, error) {
	i, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe archive install directory %s", path)
	}
	return os.ReadDir(path)
}

func archiveInstallAbsent(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrArchiveMaintenance, path)
}

func (p archiveInstallPaths) check() error {
	if p.Activation.Root != "" {
		if err := (storagewatch.Gate{Root: p.Activation.Root}).CheckInstallationAdmission(); err != nil {
			return err
		}
		if err := archiveInstallAbsent(filepath.Join(p.Activation.Root, "archive-admission")); err != nil {
			return err
		}
	}
	if p.Activation.ServiceWC != "" {
		for _, parent := range []string{p.Activation.ServiceWC, filepath.Join(p.Activation.ServiceWC, "admin")} {
			if _, err := archiveInstallDirectory(parent); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		dir := filepath.Join(p.Activation.ServiceWC, "admin", "repositories")
		entries, err := archiveInstallDirectory(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) != ".json" {
				continue
			}
			var record struct {
				Archive json.RawMessage `json:"archive_policy"`
			}
			if err := readArchiveInstallJSON(filepath.Join(dir, e.Name()), &record); err != nil {
				return err
			}
			if len(record.Archive) != 0 && !bytes.Equal(bytes.TrimSpace(record.Archive), []byte("null")) {
				return fmt.Errorf("%w: canonical repository %s", ErrArchiveMaintenance, e.Name())
			}
		}
	}
	if p.Repositories.Root != "" {
		entries, err := archiveInstallDirectory(p.Repositories.Root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			// One shallow metadata lookup per repository, never db/revs, user
			// trees or dumps. A symlink candidate is not followed.
			if e.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("unsafe repository entry %s", e.Name())
			}
			if !e.IsDir() {
				continue
			}
			hooks := filepath.Join(p.Repositories.Root, e.Name(), "hooks")
			if _, err := archiveInstallDirectory(hooks); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			if err := archiveInstallAbsent(filepath.Join(hooks, ".filees-archive-protection")); err != nil {
				return err
			}
		}
	}
	return nil
}

func readArchiveInstallJSON(path string, dst any) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() || i.Size() > 1<<20 {
		return fmt.Errorf("unsafe archive install metadata %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(i, opened) {
		return errors.New("archive install metadata identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("archive install metadata is too large")
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("archive install metadata must be a JSON object")
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("archive install metadata %s: %w", path, err)
	}
	return nil
}

func (r *Runner) archiveInstallUnveils() ([]unveilSpec, error) {
	config := filepath.Join(r.Config.SysconfDir, "server.json")
	var p archiveInstallPaths
	if err := readArchiveInstallJSON(config, &p); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	specs := []unveilSpec{{Label: "archive-server-config", Path: config, Perms: "r"}}
	if p.Activation.Root != "" {
		specs = append(specs,
			unveilSpec{Label: "archive-admission", Path: p.Activation.Root, Perms: "r"},
			unveilSpec{Label: "archive-writer-lease", Path: filepath.Join(p.Activation.Root, ".capacity-writers.lock"), Perms: "rw"})
	}
	if p.Activation.ServiceWC != "" {
		specs = append(specs, unveilSpec{Label: "archive-authority", Path: p.Activation.ServiceWC, Perms: "r"})
	}
	return specs, nil
}
