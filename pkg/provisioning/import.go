package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filees/pkg/client"
)

type ImportLimits struct {
	MaxBatchFiles int
	MaxBatchBytes int64
}

type Snapshot struct {
	Directories []string
	Files       []SnapshotFile
	TotalBytes  int64
}

type SnapshotFile struct {
	Path string
	Size int64
}

type InitialSVN interface {
	Checkout(context.Context, string, string) (string, error)
	Status(context.Context, string, []string) ([]client.StatusEntry, error)
	Add(context.Context, string, []string) (string, error)
	Commit(context.Context, string, []string, string) (string, error)
	Revision(context.Context, string) (int64, error)
}

type initialReverter interface {
	Revert(context.Context, string, []string) (string, error)
}

func ScanInitialSnapshot(root string) (Snapshot, error) {
	var snapshot Snapshot
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.Clean(rel)
		first := strings.Split(rel, string(filepath.Separator))[0]
		if first == ".svn" || first == ".filees" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %q: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("initial snapshot contains symbolic link %q", rel)
		}
		if info.IsDir() {
			snapshot.Directories = append(snapshot.Directories, rel)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("initial snapshot contains unsafe filesystem object %q (%s)", rel, fileType(info.Mode()))
		}
		snapshot.Files = append(snapshot.Files, SnapshotFile{Path: rel, Size: info.Size()})
		snapshot.TotalBytes += info.Size()
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("scan initial snapshot: %w", err)
	}
	sort.Slice(snapshot.Directories, func(i, j int) bool {
		di, dj := pathDepth(snapshot.Directories[i]), pathDepth(snapshot.Directories[j])
		if di != dj {
			return di < dj
		}
		return snapshot.Directories[i] < snapshot.Directories[j]
	})
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	return snapshot, nil
}

// PublishInitialSnapshot is restart-safe. A repeated call checks WC status and
// commits only paths that have not already reached the repository.
func PublishInitialSnapshot(ctx context.Context, store *Store, operationID, requestID string, svn InitialSVN, limits ImportLimits) (Operation, error) {
	if limits.MaxBatchFiles <= 0 || limits.MaxBatchBytes <= 0 {
		return Operation{}, errors.New("positive initial import limits are required")
	}
	op, err := store.StartInitialCommit(operationID, requestID)
	if err != nil {
		return Operation{}, err
	}
	fail := func(cause error) (Operation, error) {
		_, persistErr := store.FailInitialCommit(operationID, requestID, "INITIAL_IMPORT_FAILED", cause.Error())
		if persistErr != nil {
			return Operation{}, fmt.Errorf("%v; persist failure: %w", cause, persistErr)
		}
		return Operation{}, cause
	}
	if _, err := svn.Checkout(ctx, op.RepoURL, op.LocalPath); err != nil {
		return fail(fmt.Errorf("checkout initial repository: %w", err))
	}
	snapshot, err := ScanInitialSnapshot(op.LocalPath)
	if err != nil {
		return fail(err)
	}
	entries, err := svn.Status(ctx, op.LocalPath, nil)
	if err != nil {
		return fail(fmt.Errorf("read initial working-copy status: %w", err))
	}
	var missing []string
	for _, entry := range entries {
		if entry.Item == "missing" {
			missing = append(missing, filepath.Clean(entry.Path))
		}
	}
	if len(missing) > 0 {
		reverter, ok := svn.(initialReverter)
		if !ok {
			return fail(errors.New("initial import recovery requires SVN revert support"))
		}
		if _, err := reverter.Revert(ctx, op.LocalPath, missing); err != nil {
			return fail(fmt.Errorf("revert missing initial paths: %w", err))
		}
		entries, err = svn.Status(ctx, op.LocalPath, nil)
		if err != nil {
			return fail(fmt.Errorf("read recovered working-copy status: %w", err))
		}
	}
	status := make(map[string]string, len(entries))
	for _, entry := range entries {
		path := filepath.Clean(entry.Path)
		status[path] = entry.Item
		if entry.Item == "conflicted" || entry.Item == "missing" || entry.Item == "obstructed" {
			return fail(fmt.Errorf("unsafe working-copy status %q for %q", entry.Item, path))
		}
	}

	var addedDirs []string
	for _, path := range snapshot.Directories {
		if status[path] == "unversioned" || status[path] == "added" {
			addedDirs = append(addedDirs, path)
		}
	}
	if err := addUnversioned(ctx, svn, op.LocalPath, addedDirs, status); err != nil {
		return fail(err)
	}

	pending := make([]SnapshotFile, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		if status[file.Path] == "unversioned" || status[file.Path] == "added" {
			pending = append(pending, file)
		}
	}
	firstCommit := true
	for len(pending) > 0 {
		batch, rest := takeInitialBatch(pending, limits)
		paths := filePaths(batch)
		if err := addUnversioned(ctx, svn, op.LocalPath, paths, status); err != nil {
			return fail(err)
		}
		if firstCommit {
			paths = append(append([]string{}, addedDirs...), paths...)
			firstCommit = false
		}
		if _, err := svn.Commit(ctx, op.LocalPath, paths, "FileES initial import"); err != nil {
			return fail(fmt.Errorf("commit initial snapshot: %w", err))
		}
		pending = rest
	}
	if firstCommit && len(addedDirs) > 0 {
		if _, err := svn.Commit(ctx, op.LocalPath, addedDirs, "FileES initial import"); err != nil {
			return fail(fmt.Errorf("commit initial directories: %w", err))
		}
	}
	// A successful commit can leave the WC root at revision 0 while added
	// children are already revision 1 (a normal mixed-revision WC). Repository
	// HEAD is the authoritative published snapshot revision.
	revision, err := svn.Revision(ctx, op.RepoURL)
	if err != nil {
		return fail(fmt.Errorf("read published revision: %w", err))
	}
	return store.MarkInitialSnapshotPublished(operationID, requestID, revision, len(snapshot.Directories)+len(snapshot.Files))
}

func addUnversioned(ctx context.Context, svn InitialSVN, root string, paths []string, status map[string]string) error {
	var unversioned []string
	for _, path := range paths {
		if status[path] == "unversioned" {
			unversioned = append(unversioned, path)
		}
	}
	if len(unversioned) == 0 {
		return nil
	}
	if _, err := svn.Add(ctx, root, unversioned); err != nil {
		return fmt.Errorf("schedule initial paths: %w", err)
	}
	for _, path := range unversioned {
		status[path] = "added"
	}
	return nil
}

func takeInitialBatch(files []SnapshotFile, limits ImportLimits) ([]SnapshotFile, []SnapshotFile) {
	count, bytes := 0, int64(0)
	for count < len(files) && count < limits.MaxBatchFiles {
		if count > 0 && bytes+files[count].Size > limits.MaxBatchBytes {
			break
		}
		bytes += files[count].Size
		count++
	}
	return files[:count], files[count:]
}

func filePaths(files []SnapshotFile) []string {
	paths := make([]string, len(files))
	for i := range files {
		paths[i] = files[i].Path
	}
	return paths
}

func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(filepath.Separator))
}
