package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
)

func inspectPreservedCopies(ctx context.Context, store *localrepo.Store, key reposupervisor.Key) error {
	svn := client.New(client.Options{SvnPath: "svn", NativeSVNPath: nativeSVNPath(), Timeout: 30 * time.Second})
	for _, record := range store.List() {
		if record.ServerID != key.ServerID || record.RepoID != key.RepoID || !record.RemoteDeletionObserved || record.RemoteCleanupStarted {
			continue
		}
		status := "unknown"
		// Plain local svn status: no -u, no network, no cleanup or revert.
		entries, err := svn.Status(ctx, record.LocalPath, nil)
		if err == nil {
			status = "clean"
			for _, entry := range entries {
				path := entry.Path
				if filepath.IsAbs(path) {
					path, _ = filepath.Rel(record.LocalPath, path)
				}
				path = filepath.ToSlash(filepath.Clean(path))
				if path == ".filees" || strings.HasPrefix(path, ".filees/") {
					continue
				}
				if reservationStatusHasLocalChanges(entry) {
					status = "changed"
					break
				}
			}
		}
		if record.PreservedAlternatePath != "" {
			// A relocation may have left two copies. Do not call this clean
			// merely because the original path has no SVN changes.
			status = "unknown"
		}
		if err := store.RecordPreservedCopyStatus(record.OperationID, status); err != nil {
			return err
		}
	}
	return nil
}

// Called only after both the supervisor and provisioner have stopped this WC.
func cleanupRemoteDeletedCopies(ctx context.Context, store *localrepo.Store, key reposupervisor.Key) error {
	var pending []error
	for _, record := range store.List() {
		if record.ServerID != key.ServerID || record.RepoID != key.RepoID || !record.RemoteDeletionObserved || record.LocalCleanupCompleted {
			continue
		}
		if err := store.CheckRemoteCleanupPaths(record.OperationID); err != nil {
			pending = append(pending, err, store.RecordRemoteCleanupError(record.OperationID, err))
			continue
		}
		if !record.RemoteCleanupStarted {
			if err := inspectPreservedCopies(ctx, store, key); err != nil {
				return err
			}
			current, _ := store.Get(record.OperationID)
			if err := store.BeginRemoteCleanup(record.OperationID, current.PreservedCopyStatus); err != nil {
				return err
			}
		}
		paths := []string{record.LocalPath}
		if record.PreservedAlternatePath != "" && record.PreservedAlternatePath != record.LocalPath {
			paths = append(paths, record.PreservedAlternatePath)
		}
		var err error
		for _, path := range paths {
			if err = stripWorkingCopyMetadataWithRetry(ctx, path, record.DetachOperationID); err != nil {
				break
			}
		}
		if err != nil {
			pending = append(pending, err, store.RecordRemoteCleanupError(record.OperationID, err))
			continue
		}
		if err := store.CompleteRemoteCleanup(record.OperationID); err != nil {
			pending = append(pending, err)
		}
	}
	return errors.Join(pending...)
}
