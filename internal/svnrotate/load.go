package svnrotate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// LoadConfig controls LoadGeneration. Unlike Config there is no trigger: the
// caller (LOAD_REPOSITORY_DUMP's worker) has already decided a load must
// happen, so size/age thresholds do not apply here.
type LoadConfig struct {
	RepoPath   string
	ArchiveDir string
	BreakLocks bool // proceed despite active locks (edit passports die)
}

func (c *LoadConfig) Validate() error {
	if !filepath.IsAbs(c.RepoPath) {
		return fmt.Errorf("repo path must be absolute, got %q", c.RepoPath)
	}
	if !filepath.IsAbs(c.ArchiveDir) {
		return fmt.Errorf("archive dir must be absolute, got %q", c.ArchiveDir)
	}
	c.RepoPath = filepath.Clean(c.RepoPath)
	c.ArchiveDir = filepath.Clean(c.ArchiveDir)
	if c.RepoPath == c.ArchiveDir {
		return fmt.Errorf("repo and archive must be different directories")
	}
	if _, err := os.Stat(filepath.Join(c.RepoPath, "format")); err != nil {
		return fmt.Errorf("%s does not look like an SVN repository: %w", c.RepoPath, err)
	}
	return nil
}

// LoadGeneration replaces cfg.RepoPath's FSFS with a fresh generation built
// from dump - an SVN dump stream the caller has already extracted from the
// carrier commit and, if requested, already run through the ignore-policy
// filter and/or bounded to keep_last_revisions
// (LOAD_REPOSITORY_DUMP_CONCEPT.md §5.3, §5.4). LoadGeneration itself does
// none of that preparation; it only builds, verifies and atomically installs
// the result, reusing the exact staging/verify/swap/archive/rollback
// discipline as Rotate (SVN_ROTATOR_CONCEPT_V2.md) — this is the "użycie
// rotatora" required by todo-control/plan/EXECUTION_ORDER.md Etap 3, not a
// parallel reimplementation.
//
// cfg.RepoPath is expected to be a fresh, single-carrier-commit repository,
// never a live one with real history — the caller enforces that precondition
// before calling here; LoadGeneration does not re-derive it.
//
// --ignore-uuid applies here for the same reason as in Rotate: the new
// generation gets its own fresh UUID, never the UUID embedded in dump.
func LoadGeneration(cfg LoadConfig, dump io.Reader, reason string, logw io.Writer) (meta Meta, err error) {
	logf := func(format string, a ...any) {
		fmt.Fprintf(logw, "filees-load-dump: "+format+"\n", a...)
	}
	if err := cfg.Validate(); err != nil {
		return Meta{}, err
	}
	if err := os.MkdirAll(cfg.ArchiveDir, 0o750); err != nil {
		return Meta{}, fmt.Errorf("archive dir: %w", err)
	}
	if same, err := sameFilesystem(filepath.Dir(cfg.RepoPath), cfg.ArchiveDir); err != nil {
		return Meta{}, err
	} else if !same {
		return Meta{}, fmt.Errorf("repo parent %s and archive %s are on different filesystems; the swap rename requires one",
			filepath.Dir(cfg.RepoPath), cfg.ArchiveDir)
	}

	release, err := acquireLock(filepath.Join(cfg.ArchiveDir, ".rotate.lock"))
	if err != nil {
		return Meta{}, err
	}
	defer release()

	head, err := headRev(cfg.RepoPath)
	if err != nil {
		return Meta{}, err
	}

	// 1. Maintenance window: block commits, same as Rotate. From here on,
	// any error path must restore the original hook.
	origHook, err := installBlockHook(cfg.RepoPath)
	if err != nil {
		return Meta{}, fmt.Errorf("block hook: %w", err)
	}
	blockActive := true
	defer func() {
		if blockActive {
			if rerr := removeBlockHook(cfg.RepoPath, origHook); rerr != nil {
				err = errors.Join(err, fmt.Errorf("restoring pre-commit hook: %w", rerr))
			}
		}
	}()
	logf("maintenance: commits blocked (carrier head=r%d)", head)

	// 2. Active locks are live edit passports; a carrier repo should never
	// have any, but the check stays for the same reason it stays in Rotate:
	// defensive, not a formality.
	locks, err := activeLocks(cfg.RepoPath)
	if err != nil {
		return Meta{}, err
	}
	if locks != "" && !cfg.BreakLocks {
		return Meta{}, fmt.Errorf("active locks present on carrier repository; close them or pass -break-locks:\n%s", locks)
	}

	workDir, err := os.MkdirTemp(cfg.ArchiveDir, ".load-work-*")
	if err != nil {
		return Meta{}, err
	}
	defer os.RemoveAll(workDir)

	tag := archiveTag()
	manifestWork := filepath.Join(workDir, tag+".log.xml")
	logf("writing carrier history manifest")
	if err := writeArtifact(manifestWork, func(w io.Writer) error {
		return writeLogXML(cfg.RepoPath, w)
	}); err != nil {
		return Meta{}, fmt.Errorf("manifest: %w", err)
	}

	// 3. New generation, built from the caller-supplied dump stream — the
	// one real difference from Rotate, which instead dumps its own HEAD.
	newRepo := filepath.Join(workDir, "new.svn")
	logf("building new generation from supplied dump")
	if err := svnadminCreate(newRepo); err != nil {
		return Meta{}, err
	}
	if err := runTool(dump, io.Discard, "svnadmin", "load", "--quiet", "--ignore-uuid", newRepo); err != nil {
		return Meta{}, fmt.Errorf("svnadmin load: %w", err)
	}

	// 4. Operational configuration: a carrier repo has no meaningful conf/
	// hooks of its own to inherit (it never had any beyond FileES's own
	// defaults), so unlike Rotate there is nothing to copy from the old
	// generation here — newRepo keeps svnadmin create's own conf/, which the
	// caller overwrites with the canonical data-authz configuration exactly
	// as it does for any freshly created repository.

	// 5. Prove the new generation before touching the hot path.
	logf("verifying new generation")
	if err := verify(newRepo); err != nil {
		return Meta{}, fmt.Errorf("verify new generation: %w", err)
	}

	oldUUID, err := repoUUID(cfg.RepoPath)
	if err != nil {
		return Meta{}, err
	}
	newUUID, err := repoUUID(newRepo)
	if err != nil {
		return Meta{}, err
	}

	// 6. The swap, identical to Rotate's: archive target must not exist — a
	// tag collision is an error, never an overwrite.
	archiveRepo := filepath.Join(cfg.ArchiveDir, tag+".svn")
	if _, err := os.Lstat(archiveRepo); err == nil {
		return Meta{}, fmt.Errorf("archive target %s already exists", archiveRepo)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Meta{}, err
	}
	logf("switching generations")
	if err := os.Rename(cfg.RepoPath, archiveRepo); err != nil {
		return Meta{}, fmt.Errorf("archive rename: %w", err)
	}
	if err := os.Rename(newRepo, cfg.RepoPath); err != nil {
		installErr := fmt.Errorf("install rename: %w", err)
		if rerr := os.Rename(archiveRepo, cfg.RepoPath); rerr != nil {
			return Meta{}, errors.Join(installErr, fmt.Errorf(
				"ROLLBACK FAILED, hot repo is at %s: %w", archiveRepo, rerr))
		}
		return Meta{}, installErr
	}
	blockActive = false
	if err := fsyncDir(filepath.Dir(cfg.RepoPath)); err != nil {
		return Meta{}, err
	}
	if err := fsyncDir(cfg.ArchiveDir); err != nil {
		return Meta{}, err
	}

	// 7. Archive artifacts: manifest, meta, FROZEN marker — same discipline
	// as Rotate, minus the optional bounded dump (LOAD_REPOSITORY_DUMP has
	// its own keep_last_revisions bound, applied upstream to the stream
	// that already went into the new generation, so there is nothing
	// further to bound here).
	if err := os.Rename(manifestWork, filepath.Join(cfg.ArchiveDir, tag+".log.xml")); err != nil {
		return Meta{}, fmt.Errorf("manifest into archive: %w", err)
	}
	result := Meta{
		Tag:        tag,
		RotatedAt:  time.Now().UTC().Format(time.RFC3339),
		OldUUID:    oldUUID,
		NewUUID:    newUUID,
		OldHead:    head,
		Reason:     reason,
		ArchiveDir: archiveRepo,
	}
	metaData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return Meta{}, err
	}
	if err := writeFileExcl(filepath.Join(cfg.ArchiveDir, tag+".meta.json"),
		append(metaData, '\n'), 0o640); err != nil {
		return Meta{}, fmt.Errorf("meta.json: %w", err)
	}
	frozen := fmt.Sprintf("Frozen FileES repository generation.\nTag: %s\nUUID: %s\nHead: r%d\nCommits are permanently blocked by hooks/pre-commit.\n",
		tag, oldUUID, head)
	if err := writeFileExcl(filepath.Join(archiveRepo, "FROZEN"), []byte(frozen), 0o444); err != nil {
		return Meta{}, fmt.Errorf("FROZEN marker: %w", err)
	}
	if err := fsyncDir(cfg.ArchiveDir); err != nil {
		return Meta{}, err
	}

	logf("done: new generation uuid=%s (was carrier uuid=%s r%d); archive=%s",
		newUUID, oldUUID, head, archiveRepo)
	return result, nil
}
