//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"filees/pkg/client"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

// A placeholder's pinned revision is not the WC BASE or current HEAD. An
// older identity is admissible only when the server proves this exact file
// has not changed (including replacement) since that pinned revision.
func confirmAnchorDeleteSelection(ctx context.Context, svn client.Client, record localrepo.Record, tree anchorRemoteTree, identity string) (string, error) {
	rel, base, size, ok := parseAnchorRemoteIdentity(identity)
	if !ok || base <= 0 || base > tree.Revision {
		return "", errors.New("file deletion has an invalid pinned revision")
	}
	var selected *anchorRemoteNode
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		if strings.EqualFold(n.Path, rel) {
			if selected != nil || n.Path != rel || n.Kind != "file" || n.Size != size {
				return "", errors.New("file deletion selection differs from the current manifest")
			}
			selected = n
		}
	}
	if selected == nil {
		return "", errors.New("file deletion selection is absent from the current manifest")
	}
	if selected.Identity == identity && base == tree.Revision {
		return identity, nil
	}
	p, rev, bytes, valid := parseAnchorRemoteIdentity(selected.Identity)
	if !valid || p != rel || rev < base || rev > tree.Revision || bytes != size {
		return "", errors.New("file deletion manifest identity is incompatible")
	}
	reader, ok := svn.(interface {
		HistoryLog(context.Context, string, int64, int64, int) ([]client.HistoryCommit, error)
	})
	if !ok {
		return "", errors.New("file deletion cannot verify the older placeholder revision")
	}
	span := tree.Revision - base
	if span <= 0 || span > 256 {
		return "", errors.New("file deletion revision proof exceeds its bounded history; refresh the placeholder first")
	}
	rows, err := reader.HistoryLog(ctx, record.RepoURL, tree.Revision, base+1, int(span))
	if err != nil {
		return "", fmt.Errorf("verify file deletion revision: %w", err)
	}
	// A parent copy/replacement can leave a child's last-changed revision
	// unchanged. Check every intervening root revision, including ancestors,
	// instead of relying on size or the child's last-changed field alone.
	if len(rows) != int(span) {
		return "", errors.New("file deletion revision history is incomplete")
	}
	for i, row := range rows {
		if row.Revision != tree.Revision-int64(i) || len(row.Changes) == 0 {
			return "", errors.New("file deletion revision history is incomplete")
		}
		for _, change := range row.Changes {
			p := strings.ToLower(change.Path)
			if p == "" || strings.EqualFold(rel, p) || strings.HasPrefix(strings.ToLower(rel), p+"/") {
				return "", errors.New("file or its parent changed on the server since this placeholder; refresh before deleting")
			}
		}
	}
	return selected.Identity, ctx.Err()
}

// Called under the real WC writer, after CFAPI/content proof. Only an
// explicitly releasable, inactive row and the local token allow an RPC.
// The native RPC verifies that exact token without force and reports
// its result; a new foreign token is never substituted or retried.
func releaseAnchorDeleteLock(ctx context.Context, repo *ipcserver.RepoState, svn client.Client, wc, rel string, localToken string) error {
	snapshot, err := repo.ListReservations(ctx)
	if err != nil {
		return err
	}
	for _, row := range snapshot.Reservations {
		if row.Path != rel {
			continue
		}
		if snapshot.Unknown || snapshot.Stale || snapshot.Offline || snapshot.Detached || !row.CanRelease || row.ActivePassport || row.LocalChanges || row.Token == "" {
			return errors.New("file reservation is not safely releasable; close editing and refresh reservations")
		}
		releaser, uok := svn.(client.LockReleaser)
		if !uok {
			return errors.New("file deletion requires token-confirmed reservation release")
		}
		if localToken == "" || localToken != row.Token {
			return errors.New("file reservation changed or is not held by this working copy; refresh reservations")
		}
		proof, _, err := releaser.ReleaseLock(ctx, wc, rel, row.Token)
		if err != nil {
			return err
		}
		if proof == nil || proof.Token != row.Token || proof.State != "released" {
			return errors.New("file reservation release was not confirmed; deletion refused")
		}
		return ctx.Err()
	}
	// No cached row is not lock absence proof. The strict native metadata
	// inspection and repository editor below still reject any surviving lock.
	if localToken != "" {
		return errors.New("file reservation is absent from the verified releasable inventory; refresh reservations")
	}
	return nil
}

func (m *anchorManager) recordFileDeleteFailure(ctx context.Context, record localrepo.Record, identity string, cause error) {
	if cause == nil || ctx.Err() != nil {
		return // A planned provider shutdown is not a failed user action.
	}
	if m.lifecycle != nil && m.currentReleaseRecord(record) != nil {
		return
	}
	rel, _, _, ok := parseAnchorRemoteIdentity(identity)
	if !ok {
		return
	}
	guards, err := guardAnchorReleaseDirectories(filepath.Join(record.LocalPath, ".filees", "logs"), record.LocalPath)
	if err != nil {
		m.log.Warnf("file deletion diagnostic unavailable: %v", err)
		return
	}
	defer closeAnchorReleaseGuards(guards)
	sink, err := openRepoErrorSink(filepath.Join(record.LocalPath, ".filees", "logs", "errors.jsonl"), "anchor-delete:"+record.RepoID)
	if err != nil {
		m.log.Warnf("file deletion diagnostic unavailable: %v", err)
		return
	}
	sink.Emit(errmap.Classify(errcat.New("repo.file_delete_failed", nil, fmt.Errorf("%s: %w", rel, cause))))
	if m.activityChanged != nil {
		m.activityChanged(record.RepoID)
	}
}
