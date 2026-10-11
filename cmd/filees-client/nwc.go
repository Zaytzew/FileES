package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type nwcLocal struct {
	path string
	kind string
}

type nwcChange struct {
	rel, spool, hash string
}

func cmdSyncNoWC(cfg Config, spec workingCopySpec, values runFlags) int {
	if !values.sandboxLocked {
		if err := checkNWCSource(spec.Path, cfg.effectiveLogDir()); err != nil {
			fmt.Fprintln(os.Stderr, "sync -nwc:", err)
			return 2
		}
		if err := lockPublishSandbox(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "sync -nwc:", err)
			return 1
		}
	}
	// The named publisher already validates canonical source/state paths
	// before locking unveil. Repeating EvalSymlinks afterwards would require
	// read access to unrelated state ancestors (e.g. /home for a /var source).
	if err := prepareSSHPath(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "sync -nwc:", err)
		return 1
	}
	lock, err := acquireFileLock(filepath.Join(cfg.effectiveLogDir(), spec.ID+".nwc.lock"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync -nwc:", err)
		return 1
	}
	defer lock.Close()
	op := "sync-nwc"
	if values.reverseReconcileSet {
		op = "reverse-reconcile-set"
	} else if values.restoreSet {
		op = "restore-set"
	}
	if err := appendRunLog(cfg.LogDir, spec.ID, op, "started", false); err != nil {
		fmt.Fprintln(os.Stderr, "sync -nwc journal:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), values.timeout)
	defer cancel()
	err = syncWithoutWC(ctx, cfg, spec, values)
	stage := "complete"
	if err != nil {
		stage = "failed"
	}
	if logErr := appendRunLog(cfg.LogDir, spec.ID, op, stage, err == nil); logErr != nil {
		fmt.Fprintln(os.Stderr, "sync -nwc journal:", logErr)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync -nwc:", err)
		return 1
	}
	return 0
}

func checkNWCSource(root, logDir string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) == filepath.Dir(filepath.Clean(root)) {
		return errors.New("-nwc source must be an absolute non-root directory")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("-nwc source must be a real directory")
	}
	if _, err := os.Lstat(filepath.Join(root, ".svn")); err == nil {
		return errors.New("-nwc source already contains .svn")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	actualRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	actualLogDir, err := filepath.EvalSymlinks(logDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(actualRoot, actualLogDir)
	if err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return errors.New("journal directory must be outside the -nwc source")
	}
	return nil
}

func readNWCLocal(root string) (map[string]nwcLocal, error) {
	out := map[string]nwcLocal{}
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if abs == root {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !safeNWCRel(rel) || d.Name() == ".svn" {
			return fmt.Errorf("unsafe local path %q", rel)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in -nwc source: %s", rel)
		}
		kind := "file"
		if d.IsDir() {
			kind = "dir"
		} else if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular file in -nwc source: %s", rel)
		}
		out[rel] = nwcLocal{path: abs, kind: kind}
		return nil
	})
	return out, err
}

func syncWithoutWC(ctx context.Context, cfg Config, spec workingCopySpec, values runFlags) error {
	nwcProgress(values.progress, "reading repository and scanning source")
	workDir, err := prepareNWCTemp(cfg.effectiveLogDir(), spec.ID)
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	remote, err := newNWCRemote(cfg, spec.URL)
	if err != nil {
		return err
	}
	rev, err := remote.revision(ctx)
	if err != nil {
		return err
	}
	head, err := remote.list(ctx, rev)
	if err != nil {
		return err
	}
	local, err := readNWCLocal(spec.Path)
	if err != nil {
		return err
	}
	if values.reverseReconcileSet {
		return reverseReconcileNWC(ctx, remote, rev, head, local, spec.Path, workDir)
	}
	return publishNWC(ctx, remote, rev, head, local, workDir, values.message, values.restoreSet, values.progress)
}

// A killed process cannot run defer. The next invocation under the per-ID
// lock removes only this ID's private spool before it stages fresh bytes.
func prepareNWCTemp(logDir, id string) (string, error) {
	if !safeCopyID(id) || !filepath.IsAbs(logDir) {
		return "", errors.New("invalid -nwc spool location")
	}
	root, err := filepath.Abs(logDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, id+".nwc-tmp")
	rel, err := filepath.Rel(root, target)
	if err != nil || rel != id+".nwc-tmp" {
		return "", errors.New("invalid -nwc spool target")
	}
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", err
	}
	return target, nil
}

func publishNWC(ctx context.Context, remote nwcRemote, rev int64, head map[string]nwcEntry, local map[string]nwcLocal, workDir, message string, restore bool, progress ...io.Writer) error {
	var out io.Writer
	if len(progress) > 0 {
		out = progress[0]
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("commit message is empty")
	}
	files := make([]string, 0, len(local))
	missing := false
	for rel, entry := range local {
		if entry.kind != "file" {
			continue
		}
		files = append(files, rel)
		if _, ok := head[rel]; !ok {
			missing = true
		}
	}
	sort.Strings(files)
	var deleted map[string]bool
	var err error
	if missing && !restore {
		deleted, err = remote.deletedPaths(ctx, rev)
		if err != nil {
			return err
		}
	}
	temp, err := os.MkdirTemp(workDir, ".nwc-publish-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	var changes []nwcChange
	var skipped int
	neededDirs := map[string]bool{}
	nwcProgress(out, "comparing and staging %d files", len(files))
	lastProgress := time.Now()
	for index, rel := range files {
		if index%10 == 0 || time.Since(lastProgress) >= 2*time.Second {
			nwcProgress(out, "checked=%d/%d changed=%d skipped-deleted=%d", index, len(files), len(changes), skipped)
			lastProgress = time.Now()
		}
		entry := local[rel]
		var existingHash string
		if current, ok := head[rel]; ok {
			if current.kind != "file" {
				return fmt.Errorf("repository path %s is a directory", rel)
			}
			localHash, err := hashNWCFile(entry.path)
			if err != nil {
				return err
			}
			existingHash, err = remote.remoteHash(ctx, rel, rev)
			if err != nil {
				return err
			}
			if localHash == existingHash {
				continue
			}
		} else if !restore && wasNWCDeleted(rel, deleted, head) {
			skipped++
			continue
		}
		for parent := pathParent(rel); parent != ""; parent = pathParent(parent) {
			if existing, ok := head[parent]; ok {
				if existing.kind != "dir" {
					return fmt.Errorf("repository parent %s is a file", parent)
				}
				break
			}
			neededDirs[parent] = true
		}
		spool := filepath.Join(temp, strconv.Itoa(len(changes))+".bin")
		hash, err := snapshotNWCFile(entry.path, spool)
		if err != nil {
			return err
		}
		if existingHash != "" && hash == existingHash {
			_ = os.Remove(spool)
			continue
		}
		changes = append(changes, nwcChange{rel: rel, spool: spool, hash: hash})
	}
	nwcProgress(out, "checked=%d/%d changed=%d skipped-deleted=%d", len(files), len(files), len(changes), skipped)
	if len(changes) == 0 {
		fmt.Fprintf(os.Stdout, "clean skipped-deleted=%d\n", skipped)
		return nil
	}
	dirs := make([]string, 0, len(neededDirs))
	for dir := range neededDirs {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if strings.Count(dirs[i], "/") != strings.Count(dirs[j], "/") {
			return strings.Count(dirs[i], "/") < strings.Count(dirs[j], "/")
		}
		return dirs[i] < dirs[j]
	})
	args := make([]string, 0, 2*len(dirs)+3*len(changes))
	for _, dir := range dirs {
		args = append(args, "mkdir", encodedNWCRel(dir))
	}
	for _, change := range changes {
		args = append(args, "put", change.spool, encodedNWCRel(change.rel))
	}
	actionFile := filepath.Join(temp, "actions.txt")
	if err := os.WriteFile(actionFile, []byte(strings.Join(args, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := ensureNWCDescriptorLimit(len(changes)); err != nil {
		return err
	}
	nwcProgress(out, "committing %d files atomically", len(changes))
	if err := remote.commit(ctx, rev, message, actionFile); err != nil {
		// A lost reply can follow a successful atomic commit. Confirm the
		// desired bytes at HEAD before deciding that this invocation failed.
		checkCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		nwcProgress(out, "confirming repository after uncertain commit reply")
		defer cancel()
		if ok, checkErr := nwcChangesAtHead(checkCtx, remote, changes); checkErr == nil && ok {
			fmt.Fprintf(os.Stdout, "published=%d skipped-deleted=%d (confirmed after lost reply)\n", len(changes), skipped)
			return nil
		}
		return err
	}
	fmt.Fprintf(os.Stdout, "published=%d skipped-deleted=%d\n", len(changes), skipped)
	return nil
}

func nwcProgress(out io.Writer, format string, args ...any) {
	if out != nil {
		fmt.Fprintf(out, "publish: "+format+"\n", args...)
	}
}

func nwcChangesAtHead(ctx context.Context, remote nwcRemote, changes []nwcChange) (bool, error) {
	rev, err := remote.revision(ctx)
	if err != nil {
		return false, err
	}
	head, err := remote.list(ctx, rev)
	if err != nil {
		return false, err
	}
	for _, change := range changes {
		if head[change.rel].kind != "file" {
			return false, nil
		}
		hash, err := remote.remoteHash(ctx, change.rel, rev)
		if err != nil {
			return false, err
		}
		if hash != change.hash {
			return false, nil
		}
	}
	return true, nil
}

func wasNWCDeleted(rel string, deleted map[string]bool, head map[string]nwcEntry) bool {
	for p := rel; p != ""; p = pathParent(p) {
		if deleted[p] && head[p].kind == "" {
			return true
		}
	}
	return false
}

func pathParent(rel string) string {
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return ""
	}
	return rel[:i]
}

func hashNWCFile(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func snapshotNWCFile(source, target string) (string, error) {
	before, err := os.Lstat(source)
	if err != nil || !before.Mode().IsRegular() {
		return "", fmt.Errorf("source changed before snapshot: %s: %v", source, err)
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", fmt.Errorf("source changed while opening: %s", source)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, h), in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return "", err
	}
	after, err := os.Lstat(source)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("source changed during snapshot: %s", source)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func encodedNWCRel(rel string) string {
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = urlPathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// Kept separate from path joining: SVN URL components must be escaped while
// local source names retain their original Unicode spelling.
func urlPathEscape(component string) string {
	return strings.ReplaceAll(url.PathEscape(component), "@", "%40")
}
