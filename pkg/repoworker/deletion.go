package repoworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const deletionArchiveSchema = "filees.deleted-repository/v1"

type deletionArchiveMeta struct {
	Schema      string    `json:"schema"`
	OperationID string    `json:"operation_id"`
	RepoID      string    `json:"repo_id"`
	DumpFile    string    `json:"dump_file"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	DeleteAfter time.Time `json:"delete_after"`
}

// DeletionRecoveryArchive resolves one exact deletion receipt into the
// capability-neutral descriptor exposed by a recovery manifest. It never
// scans archive names supplied by a client.
func DeletionRecoveryArchive(root, repoID, operationID string) (RecoveryArchive, time.Time, bool, error) {
	if !filepath.IsAbs(root) {
		return RecoveryArchive{}, time.Time{}, false, errors.New("repository deletion archive root must be absolute")
	}
	if _, err := uuid.Parse(repoID); err != nil {
		return RecoveryArchive{}, time.Time{}, false, errors.New("repository ID must be UUID")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return RecoveryArchive{}, time.Time{}, false, errors.New("repository deletion operation ID must be UUID")
	}
	base := repoID + "-" + operationID
	dumpPath := filepath.Join(root, base+".svndump")
	metaPath := filepath.Join(root, base+".json")
	meta, found, err := loadDeletionArchive(metaPath, dumpPath, repoID, operationID)
	if err != nil || !found {
		return RecoveryArchive{}, time.Time{}, found, err
	}
	info, err := os.Lstat(dumpPath)
	if err != nil {
		return RecoveryArchive{}, time.Time{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return RecoveryArchive{}, time.Time{}, false, errors.New("repository deletion dump is not a regular file")
	}
	archiveID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("filees-recovery:"+repoID+":"+operationID)).String()
	return RecoveryArchive{ArchiveID: archiveID, RepoID: repoID, SHA256: meta.SHA256, Size: info.Size()}, meta.DeleteAfter, true, nil
}

func (e ServerEffects) PrepareDelete(_ context.Context, repoID, operationID string) error {
	repo, err := e.deleteRepoPath(repoID, operationID)
	if err != nil {
		return err
	}
	if !validRepo(repo) {
		if _, statErr := os.Stat(repo); errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		return errors.New("repository selected for deletion is invalid")
	}
	hooks := filepath.Join(repo, "hooks")
	hook := filepath.Join(hooks, "pre-commit")
	original := filepath.Join(hooks, "pre-commit.filees-delete-"+operationID+".original")
	marker := "FileES repository deletion " + operationID
	if raw, readErr := os.ReadFile(hook); readErr == nil && strings.Contains(string(raw), marker) {
		return nil
	}
	if _, statErr := os.Lstat(original); errors.Is(statErr, os.ErrNotExist) {
		if _, hookErr := os.Lstat(hook); hookErr == nil {
			if err := os.Rename(hook, original); err != nil {
				return err
			}
		} else if !errors.Is(hookErr, os.ErrNotExist) {
			return hookErr
		}
	} else if statErr != nil {
		return statErr
	}
	body := []byte("#!/bin/sh\nprintf '%s\\n' '" + marker + ": commits are blocked' >&2\nexit 1\n")
	temp, err := os.CreateTemp(hooks, ".filees-delete-hook-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Chmod(0700); err == nil {
		_, err = temp.Write(body)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tempPath, hook); err != nil {
		return err
	}
	return syncDirectory(hooks)
}

func (e ServerEffects) ArchiveAndDeleteFSFS(ctx context.Context, repoID, operationID string) (time.Time, error) {
	repo, err := e.deleteRepoPath(repoID, operationID)
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now().UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	if e.DeletionRetentionDays < 0 {
		return time.Time{}, errors.New("repository deletion retention cannot be negative")
	}
	if e.DeletionRetentionDays == 0 {
		return now, removeRepositoryTree(repo)
	}
	if !filepath.IsAbs(e.DeletionArchiveRoot) {
		return time.Time{}, errors.New("repository deletion archive root must be absolute")
	}
	if err := os.MkdirAll(e.DeletionArchiveRoot, 0700); err != nil {
		return time.Time{}, err
	}
	base := repoID + "-" + operationID
	dumpPath := filepath.Join(e.DeletionArchiveRoot, base+".svndump")
	metaPath := filepath.Join(e.DeletionArchiveRoot, base+".json")
	if meta, ok, err := loadDeletionArchive(metaPath, dumpPath, repoID, operationID); err != nil {
		return time.Time{}, err
	} else if ok {
		if err := removeRepositoryTree(repo); err != nil {
			return time.Time{}, err
		}
		return meta.DeleteAfter, nil
	}

	if _, err := os.Stat(dumpPath); errors.Is(err, os.ErrNotExist) {
		if err := e.createDump(ctx, repo, dumpPath, operationID); err != nil {
			return time.Time{}, err
		}
	} else if err != nil {
		return time.Time{}, err
	}
	if err := e.verifyDump(ctx, dumpPath, operationID); err != nil {
		return time.Time{}, err
	}
	digest, err := fileDigest(dumpPath)
	if err != nil {
		return time.Time{}, err
	}
	deleteAfter := now.Add(time.Duration(e.DeletionRetentionDays) * 24 * time.Hour)
	meta := deletionArchiveMeta{
		Schema: deletionArchiveSchema, OperationID: operationID, RepoID: repoID,
		DumpFile: filepath.Base(dumpPath), SHA256: digest, CreatedAt: now, DeleteAfter: deleteAfter,
	}
	if err := atomicJSON(metaPath, meta); err != nil {
		return time.Time{}, err
	}
	if err := removeRepositoryTree(repo); err != nil {
		return time.Time{}, err
	}
	return deleteAfter, nil
}

func (e ServerEffects) createDump(ctx context.Context, repo, finalPath, operationID string) error {
	if !validRepo(repo) {
		return errors.New("repository selected for dump is invalid")
	}
	tempPath := finalPath + ".tmp-" + operationID
	_ = os.Remove(tempPath)
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	// freeze is the write barrier for the final snapshot. The pre-commit hook
	// rejects transactions that arrive while verification is running; freeze
	// additionally waits for a transaction already in flight and holds the
	// repository write lock for the complete dump.
	command := exec.CommandContext(ctx, e.SVNAdmin,
		"freeze", repo, "--",
		e.SVNAdmin, "dump", repo, "--quiet",
	)
	command.Stdout, command.Stderr = file, &stderr
	runErr := command.Run()
	if runErr == nil {
		runErr = file.Sync()
	}
	if closeErr := file.Close(); runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("svnadmin dump: %w: %s", runErr, stderr.String())
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return syncDirectory(filepath.Dir(finalPath))
}

func (e ServerEffects) verifyDump(ctx context.Context, dumpPath, operationID string) error {
	verifyRoot := filepath.Join(e.RepositoriesRoot, ".verify-delete-"+operationID)
	if err := os.RemoveAll(verifyRoot); err != nil {
		return err
	}
	defer os.RemoveAll(verifyRoot)
	if output, err := exec.CommandContext(ctx, e.SVNAdmin, "create", verifyRoot).CombinedOutput(); err != nil {
		return fmt.Errorf("create dump verifier: %w: %s", err, output)
	}
	dump, err := os.Open(dumpPath)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	load := exec.CommandContext(ctx, e.SVNAdmin, "load", verifyRoot, "--quiet")
	load.Stdin, load.Stdout, load.Stderr = dump, io.Discard, &stderr
	loadErr := load.Run()
	closeErr := dump.Close()
	if loadErr != nil {
		return fmt.Errorf("verify dump load: %w: %s", loadErr, stderr.String())
	}
	if closeErr != nil {
		return closeErr
	}
	if output, err := exec.CommandContext(ctx, e.SVNAdmin, "verify", verifyRoot, "--quiet").CombinedOutput(); err != nil {
		return fmt.Errorf("verify loaded dump: %w: %s", err, output)
	}
	return nil
}

func (e ServerEffects) deleteRepoPath(repoID, operationID string) (string, error) {
	if !filepath.IsAbs(e.SVNAdmin) || !filepath.IsAbs(e.RepositoriesRoot) {
		return "", errors.New("svnadmin and repositories root must be absolute")
	}
	if _, err := uuid.Parse(repoID); err != nil {
		return "", errors.New("repository ID must be UUID")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return "", errors.New("repository deletion operation ID must be UUID")
	}
	root := filepath.Clean(e.RepositoriesRoot)
	repo := filepath.Join(root, repoID)
	if filepath.Dir(repo) != root {
		return "", errors.New("repository deletion path escapes repository root")
	}
	return repo, nil
}

func removeRepositoryTree(repo string) error {
	info, err := os.Lstat(repo)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to remove non-directory repository tree")
	}
	if err := os.RemoveAll(repo); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(repo))
}

func loadDeletionArchive(metaPath, dumpPath, repoID, operationID string) (deletionArchiveMeta, bool, error) {
	raw, err := os.ReadFile(metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return deletionArchiveMeta{}, false, nil
	}
	if err != nil {
		return deletionArchiveMeta{}, false, err
	}
	var meta deletionArchiveMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return deletionArchiveMeta{}, false, err
	}
	if err := validateDeletionArchiveMeta(meta); err != nil || meta.RepoID != repoID || meta.OperationID != operationID ||
		meta.DumpFile != filepath.Base(dumpPath) {
		return deletionArchiveMeta{}, false, errors.New("repository deletion archive metadata is invalid")
	}
	digest, err := fileDigest(dumpPath)
	if err != nil {
		return deletionArchiveMeta{}, false, err
	}
	if digest != meta.SHA256 {
		return deletionArchiveMeta{}, false, errors.New("repository deletion dump digest mismatch")
	}
	return meta, true, nil
}

func ReapDeletionArchives(root string, now time.Time) (int, error) {
	if !filepath.IsAbs(root) {
		return 0, errors.New("repository deletion archive root must be absolute")
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		metaPath := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			return removed, err
		}
		var meta deletionArchiveMeta
		if err := json.Unmarshal(raw, &meta); err != nil || validateDeletionArchiveMeta(meta) != nil {
			return removed, fmt.Errorf("invalid deletion archive metadata %s", entry.Name())
		}
		base := meta.RepoID + "-" + meta.OperationID
		if entry.Name() != base+".json" || meta.DumpFile != base+".svndump" {
			return removed, fmt.Errorf("invalid deletion archive metadata %s", entry.Name())
		}
		if now.UTC().Before(meta.DeleteAfter) {
			continue
		}
		if err := os.Remove(filepath.Join(root, meta.DumpFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		if err := os.Remove(metaPath); err != nil {
			return removed, err
		}
		removed++
	}
	if removed > 0 {
		if err := syncDirectory(root); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func validateDeletionArchiveMeta(meta deletionArchiveMeta) error {
	if meta.Schema != deletionArchiveSchema {
		return errors.New("repository deletion archive schema is invalid")
	}
	if _, err := uuid.Parse(meta.RepoID); err != nil {
		return errors.New("repository deletion archive repo ID is invalid")
	}
	if _, err := uuid.Parse(meta.OperationID); err != nil {
		return errors.New("repository deletion archive operation ID is invalid")
	}
	if meta.CreatedAt.IsZero() || meta.DeleteAfter.Before(meta.CreatedAt) {
		return errors.New("repository deletion archive retention is invalid")
	}
	if filepath.Base(meta.DumpFile) != meta.DumpFile || meta.DumpFile == "." || meta.DumpFile == "" {
		return errors.New("repository deletion archive dump path is invalid")
	}
	if len(meta.SHA256) != sha256.Size*2 {
		return errors.New("repository deletion archive digest is invalid")
	}
	if _, err := hex.DecodeString(meta.SHA256); err != nil {
		return errors.New("repository deletion archive digest is invalid")
	}
	return nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
