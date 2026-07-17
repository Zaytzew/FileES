// Package svnrotate implements the FileES repository rotator, V2 model:
// an EXPLICIT generation change (concepts/SVN_ROTATOR_CONCEPT_V2.md).
//
// The retired V1 prototype tried to make rotation invisible by carrying the
// old UUID and padding revision numbers; the 2026-07-16 review
// (SVN_ROTATOR_REVIEW.md) showed that to be unsound. V2 makes the cut
// honest: the new generation has a NEW UUID, starts at r1 = a full dump of
// the old HEAD (which preserves versioned properties, unlike svn export),
// inherits conf/ and hooks/ explicitly, and is verified before the switch.
// Clients recheckout via FileES tooling; meta.json records the generation
// change for that orchestration.
package svnrotate

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const blockHookBody = `#!/bin/sh
echo "FileES: repository rotation in progress; commits are blocked" >&2
exit 1
`

const hookAsideSuffix = ".pre-rotate"

// Meta is the archived record of one generation change, written as
// <tag>.meta.json next to the frozen repository.
type Meta struct {
	Tag        string `json:"tag"`
	RotatedAt  string `json:"rotated_at"`
	OldUUID    string `json:"old_uuid"`
	NewUUID    string `json:"new_uuid"`
	OldHead    int    `json:"old_head"`
	Reason     string `json:"reason,omitempty"`
	ArchiveDir string `json:"archive_repo"`
	DumpRange  string `json:"dump_range,omitempty"`
}

// Rotate performs the generation change. reason is recorded in meta.json
// (the trigger description, or "forced"). Nothing touches the hot repo
// except the temporary pre-commit block hook until the final swap; every
// failure before the swap restores the hook and leaves the repo as found.
func Rotate(cfg Config, reason string, logw io.Writer) (err error) {
	logf := func(format string, a ...any) {
		fmt.Fprintf(logw, "filees-rotate: "+format+"\n", a...)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ArchiveDir, 0o750); err != nil {
		return fmt.Errorf("archive dir: %w", err)
	}
	if same, err := sameFilesystem(filepath.Dir(cfg.RepoPath), cfg.ArchiveDir); err != nil {
		return err
	} else if !same {
		return fmt.Errorf("repo parent %s and archive %s are on different filesystems; the swap rename requires one",
			filepath.Dir(cfg.RepoPath), cfg.ArchiveDir)
	}

	release, err := acquireLock(filepath.Join(cfg.ArchiveDir, ".rotate.lock"))
	if err != nil {
		return err
	}
	defer release()

	head, err := headRev(cfg.RepoPath)
	if err != nil {
		return err
	}
	if head == 0 {
		logf("repository is empty (r0 only), nothing to rotate")
		return nil
	}

	// 1. Maintenance window: block commits. From here on, any error path
	// must restore the original hook — tracked via blockActive.
	origHook, err := installBlockHook(cfg.RepoPath)
	if err != nil {
		return fmt.Errorf("block hook: %w", err)
	}
	blockActive := true
	defer func() {
		if blockActive {
			if rerr := removeBlockHook(cfg.RepoPath, origHook); rerr != nil {
				err = errors.Join(err, fmt.Errorf("restoring pre-commit hook: %w", rerr))
			}
		}
	}()
	logf("maintenance: commits blocked (head=r%d)", head)

	// 2. Active locks are live edit passports; rotation must not silently
	// destroy them.
	locks, err := activeLocks(cfg.RepoPath)
	if err != nil {
		return err
	}
	if locks != "" && !cfg.BreakLocks {
		return fmt.Errorf("active locks present (edit passports); close them or pass -break-locks:\n%s", locks)
	}
	if locks != "" {
		logf("WARNING: proceeding despite active locks (-break-locks)")
	}

	workDir, err := os.MkdirTemp(cfg.ArchiveDir, ".rotate-work-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir) // always our own fresh temp dir, never caller input

	// 3. Manifest of the full history, taken after writes stopped.
	tag := archiveTag()
	manifestWork := filepath.Join(workDir, tag+".log.xml")
	logf("writing history manifest")
	if err := writeArtifact(manifestWork, func(w io.Writer) error {
		return writeLogXML(cfg.RepoPath, w)
	}); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	// 4. New generation: r1 = complete dump of old HEAD, properties intact.
	newRepo := filepath.Join(workDir, "new.svn")
	logf("building new generation from HEAD snapshot")
	if err := svnadminCreate(newRepo); err != nil {
		return err
	}
	if err := dumpRevLoad(cfg.RepoPath, newRepo, head); err != nil {
		return err
	}

	// 5. Operational configuration is inherited explicitly, never left to
	// svnadmin create defaults.
	if err := copyTree(filepath.Join(cfg.RepoPath, "conf"), filepath.Join(newRepo, "conf")); err != nil {
		return fmt.Errorf("copy conf: %w", err)
	}
	if err := copyHooks(filepath.Join(cfg.RepoPath, "hooks"), filepath.Join(newRepo, "hooks")); err != nil {
		return fmt.Errorf("copy hooks: %w", err)
	}

	// 6. Prove the new generation before touching the hot path.
	logf("verifying new generation")
	if err := verify(newRepo); err != nil {
		return fmt.Errorf("verify new generation: %w", err)
	}
	oldTree, err := treeListing(cfg.RepoPath, head)
	if err != nil {
		return err
	}
	newTree, err := treeListing(newRepo, 1)
	if err != nil {
		return err
	}
	if oldTree != newTree {
		return fmt.Errorf("tree mismatch between old r%d and new r1; aborting before switch", head)
	}

	oldUUID, err := repoUUID(cfg.RepoPath)
	if err != nil {
		return err
	}
	newUUID, err := repoUUID(newRepo)
	if err != nil {
		return err
	}

	// 7. Optional bounded dump: the last DumpDepth revisions before HEAD,
	// per backup policy. Full history stays in the frozen FSFS archive.
	dumpWork, dumpName, dumpRange := "", "", ""
	if cfg.DumpDepth > 0 {
		low := head - cfg.DumpDepth + 1
		if low < 1 {
			low = 1
		}
		dumpRange = fmt.Sprintf("r%d:r%d", low, head)
		dumpName = fmt.Sprintf("%s.r%d-r%d.dump.gz", tag, low, head)
		dumpWork = filepath.Join(workDir, dumpName)
		logf("writing bounded dump %s", dumpRange)
		if err := writeGzArtifact(dumpWork, func(w io.Writer) error {
			return writeRangeDump(cfg.RepoPath, w, low, head)
		}); err != nil {
			return fmt.Errorf("bounded dump: %w", err)
		}
	}

	// 8. The swap. Archive target must not exist — a tag collision is an
	// error, never an overwrite.
	archiveRepo := filepath.Join(cfg.ArchiveDir, tag+".svn")
	if _, err := os.Lstat(archiveRepo); err == nil {
		return fmt.Errorf("archive target %s already exists", archiveRepo)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	logf("switching generations")
	if err := os.Rename(cfg.RepoPath, archiveRepo); err != nil {
		return fmt.Errorf("archive rename: %w", err)
	}
	if err := os.Rename(newRepo, cfg.RepoPath); err != nil {
		err = fmt.Errorf("install rename: %w", err)
		if rerr := os.Rename(archiveRepo, cfg.RepoPath); rerr != nil {
			err = errors.Join(err, fmt.Errorf(
				"ROLLBACK FAILED, hot repo is at %s: %w", archiveRepo, rerr))
		}
		return err
	}
	// The hot repo is now the new generation with the original hooks; the
	// block hook lives on only in the frozen archive, by design.
	blockActive = false
	if err := fsyncDir(filepath.Dir(cfg.RepoPath)); err != nil {
		return err
	}
	if err := fsyncDir(cfg.ArchiveDir); err != nil {
		return err
	}

	// 9. Archive artifacts: manifest, meta, optional dump, FROZEN marker.
	if err := os.Rename(manifestWork, filepath.Join(cfg.ArchiveDir, tag+".log.xml")); err != nil {
		return fmt.Errorf("manifest into archive: %w", err)
	}
	if dumpWork != "" {
		if err := os.Rename(dumpWork, filepath.Join(cfg.ArchiveDir, dumpName)); err != nil {
			return fmt.Errorf("dump into archive: %w", err)
		}
	}
	meta := Meta{
		Tag:        tag,
		RotatedAt:  time.Now().UTC().Format(time.RFC3339),
		OldUUID:    oldUUID,
		NewUUID:    newUUID,
		OldHead:    head,
		Reason:     reason,
		ArchiveDir: archiveRepo,
		DumpRange:  dumpRange,
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileExcl(filepath.Join(cfg.ArchiveDir, tag+".meta.json"),
		append(metaData, '\n'), 0o640); err != nil {
		return fmt.Errorf("meta.json: %w", err)
	}
	frozen := fmt.Sprintf("Frozen FileES repository generation.\nTag: %s\nUUID: %s\nHead: r%d\nCommits are permanently blocked by hooks/pre-commit.\n",
		tag, oldUUID, head)
	if err := writeFileExcl(filepath.Join(archiveRepo, "FROZEN"), []byte(frozen), 0o444); err != nil {
		return fmt.Errorf("FROZEN marker: %w", err)
	}
	if err := fsyncDir(cfg.ArchiveDir); err != nil {
		return err
	}

	logf("done: new generation uuid=%s at r1 (was uuid=%s r%d); archive=%s",
		newUUID, oldUUID, head, archiveRepo)
	logf("clients of this repository must be reprovisioned (recheckout)")
	return nil
}

// installBlockHook replaces hooks/pre-commit with a script refusing all
// commits, moving any existing hook aside. Returns the aside path ("" when
// there was no hook).
func installBlockHook(repoPath string) (origHook string, err error) {
	hook := filepath.Join(repoPath, "hooks", "pre-commit")
	if _, err := os.Lstat(hook); err == nil {
		origHook = hook + hookAsideSuffix
		if _, err := os.Lstat(origHook); err == nil {
			return "", fmt.Errorf("%s already exists (crashed rotation?); resolve manually", origHook)
		}
		if err := os.Rename(hook, origHook); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := writeFileExcl(hook, []byte(blockHookBody), 0o755); err != nil {
		if origHook != "" {
			err = errors.Join(err, os.Rename(origHook, hook))
		}
		return "", err
	}
	return origHook, nil
}

// removeBlockHook undoes installBlockHook on the failure path.
func removeBlockHook(repoPath, origHook string) error {
	hook := filepath.Join(repoPath, "hooks", "pre-commit")
	if err := os.Remove(hook); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if origHook != "" {
		return os.Rename(origHook, hook)
	}
	return nil
}

// copyHooks copies the old generation's hooks, skipping the temporary block
// hook and restoring the aside original under its proper name.
func copyHooks(srcHooks, dstHooks string) error {
	entries, err := os.ReadDir(srcHooks)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstHooks, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		switch {
		case name == "pre-commit":
			continue // the rotation block hook stays out of the new generation
		case name == "pre-commit"+hookAsideSuffix:
			name = "pre-commit"
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if err := copyFileTo(filepath.Join(srcHooks, e.Name()),
			filepath.Join(dstHooks, name), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

// repoUUID returns the repository UUID: the FIRST line of db/uuid. Modern
// FSFS appends an instance ID as a second line — passing both lines around
// as one string is exactly the bug that broke the V1 prototype.
func repoUUID(repoPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoPath, "db", "uuid"))
	if err != nil {
		return "", err
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	uuid := strings.TrimSpace(string(line))
	if uuid == "" {
		return "", fmt.Errorf("empty uuid in %s", repoPath)
	}
	return uuid, nil
}

// writeArtifact streams produce's output into an O_EXCL file with checked
// Close and fsync.
func writeArtifact(path string, produce func(io.Writer) error) error {
	f, err := createExcl(path, 0o640)
	if err != nil {
		return err
	}
	if err := produce(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeGzArtifact is writeArtifact with a gzip layer; gzip.Close errors are
// checked — a truncated compressed archive must fail the rotation.
func writeGzArtifact(path string, produce func(io.Writer) error) error {
	return writeArtifact(path, func(w io.Writer) error {
		gz := gzip.NewWriter(w)
		if err := produce(gz); err != nil {
			gz.Close()
			return err
		}
		return gz.Close()
	})
}

func archiveTag() string {
	now := time.Now().UTC()
	y, w := now.ISOWeek()
	return fmt.Sprintf("%04d-W%02d_%s", y, w, now.Format("20060102T150405Z"))
}
