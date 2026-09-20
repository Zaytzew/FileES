package svnrotate

import (
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
	// Prepare writes caller-owned configuration and a durable operation receipt
	// in the verified staging repository, before it becomes the hot generation.
	Prepare func(staging string, meta Meta) error
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
// none of that preparation; it only builds, verifies and installs
// the result, reusing the staging/verify/journaled-swap/archive/recovery
// discipline as Rotate (SVN_ROTATOR_CONCEPT_V2.md) — this is the "użycie
// rotatora" required by implementation notes (not distributed) Etap 3, not a
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
	if recovered, found, err := Recover(cfg.RepoPath, cfg.ArchiveDir, reason); found || err != nil {
		return recovered, err
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
	if recovered, found, err := recoverSwap(cfg.RepoPath, cfg.ArchiveDir, reason); found || err != nil {
		return recovered, err
	}

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
	prepared := false
	defer func() {
		if !prepared {
			_ = os.RemoveAll(workDir)
		}
	}()

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

	// 4. Keep operational hook policy (including FileES lock guards) from
	// the carrier, not the dump. Prepare may rebuild conf/ from canonical
	// authz before the swap. copyHooks removes only the temporary commit fence.
	if err := copyHooks(filepath.Join(cfg.RepoPath, "hooks"), filepath.Join(newRepo, "hooks")); err != nil {
		return Meta{}, fmt.Errorf("copy carrier hooks: %w", err)
	}

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

	// 6. Prepare a durable swap, identical to Rotate's: archive target must not exist — a
	// tag collision is an error, never an overwrite.
	archiveRepo := filepath.Join(cfg.ArchiveDir, tag+".svn")
	if _, err := os.Lstat(archiveRepo); err == nil {
		return Meta{}, fmt.Errorf("archive target %s already exists", archiveRepo)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Meta{}, err
	}
	result := Meta{
		Tag: tag, RotatedAt: time.Now().UTC().Format(time.RFC3339),
		OldUUID: oldUUID, NewUUID: newUUID, OldHead: head,
		Reason: reason, ArchiveDir: archiveRepo,
	}
	if cfg.Prepare != nil {
		if err := cfg.Prepare(newRepo, result); err != nil {
			return Meta{}, fmt.Errorf("prepare new generation: %w", err)
		}
	}
	transaction, err := prepareSwap(cfg.RepoPath, cfg.ArchiveDir, workDir, result, tag+".log.xml")
	if _, journalErr := os.Lstat(swapPath(cfg.RepoPath, cfg.ArchiveDir)); journalErr == nil {
		prepared = true
		blockActive = false
	}
	if err != nil {
		return Meta{}, err
	}
	prepared = true
	blockActive = false // recovery now owns the fence and staging directory
	swapCheckpoint("prepared")
	if err := transaction.finish(cfg.ArchiveDir); err != nil {
		return Meta{}, err
	}

	logf("done: new generation uuid=%s (was carrier uuid=%s r%d); archive=%s",
		newUUID, oldUUID, head, archiveRepo)
	return result, nil
}
