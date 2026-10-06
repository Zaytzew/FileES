//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

// download handles only the current durable point at the projected path.
// Ordinary copies keep their existing depth-based implementation. Even when
// the helper is missing, an anchor must not fall back to SVN on placeholders.
func (m *anchorManager) download(ctx context.Context, serverID, repoID, rel string) (bool, error) {
	if m.lifecycle == nil || m.repos == nil {
		return false, nil
	}
	repo := m.repos(serverID, repoID)
	if repo == nil {
		return false, nil
	}
	local := repo.Summary().LocalPath
	for _, record := range m.lifecycle.List() {
		if record.ServerID != serverID || record.RepoID != repoID || !record.Anchor ||
			filepath.Clean(record.LocalPath) != filepath.Clean(local) || record.State == localrepo.StateDetached {
			continue
		}
		if record.State != localrepo.StateAttached || m.helper == "" {
			return true, ipcserver.ErrDepthUnavailable
		}
		return true, m.downloadFile(ctx, record, rel)
	}
	return false, nil
}

// Hydration and adoption are separate acknowledgements. The attribute-only
// native request triggers the existing fetch bridge; never hold wcOpMu while
// waiting for it, as adoption needs that same lock. Success also requires the
// path to have arrived in SVN, checked later under the running WC guard.
func (m *anchorManager) downloadFile(ctx context.Context, record localrepo.Record, identity string) error {
	rel, ok := cleanAnchorIdentity(identity)
	if !ok {
		return errors.New("choose one file in the Explorer point; bulk download is not available yet")
	}
	answer, err := m.call(ctx, "", "hydrate", "--root", record.LocalPath, "--rel", rel)
	if err != nil {
		return err
	}
	if !answer.OK {
		return fmt.Errorf("Explorer download refused: %s (%s)", answer.Error, answer.HResult)
	}
	return m.waitDownloaded(ctx, record, rel)
}

func (m *anchorManager) waitDownloaded(ctx context.Context, record localrepo.Record, rel string) error {
	deadline, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		repo := m.repos(record.ServerID, record.RepoID)
		if repo == nil {
			return ipcserver.ErrDepthUnavailable
		}
		summary := repo.Summary()
		if !summary.Attached || filepath.Clean(summary.LocalPath) != filepath.Clean(record.LocalPath) {
			return errors.New("Explorer point was detached during download")
		}
		adopted := false
		err := repo.WithLocalStatus(deadline, []string{rel}, func(rows []client.StatusEntry) error {
			for _, row := range rows {
				name := row.Path
				if filepath.IsAbs(name) {
					name, _ = filepath.Rel(record.LocalPath, name)
				}
				if filepath.ToSlash(name) == rel {
					_, adopted = anchorSVNLocalState(row)
					break
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, commit.ErrLocalStatusBusy) {
			return err
		}
		if err == nil && adopted {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-deadline.Done():
			timer.Stop()
			return fmt.Errorf("downloaded file was not adopted into the working copy: %w", deadline.Err())
		case <-timer.C:
		}
	}
}
