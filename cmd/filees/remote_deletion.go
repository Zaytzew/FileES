package main

import (
	"context"
	"time"

	"filees/pkg/client"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
)

func inspectPreservedCopies(ctx context.Context, store *localrepo.Store, key reposupervisor.Key) error {
	svn := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Second})
	for _, record := range store.List() {
		if record.ServerID != key.ServerID || record.RepoID != key.RepoID || !record.RemoteDeletionObserved {
			continue
		}
		status := "unknown"
		// Plain local svn status: no -u, no network, no cleanup or revert.
		entries, err := svn.Status(ctx, record.LocalPath, nil)
		if err == nil {
			status = "clean"
			for _, entry := range entries {
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
