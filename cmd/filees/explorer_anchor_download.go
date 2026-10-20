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
	// CfHydratePlaceholder on an already complete file emits no FETCH_DATA.
	// Adoption must therefore also be completed by the explicit download,
	// not solely by a callback that may have been lost before a restart.
	return m.finishDownloaded(ctx, record, rel)
}

func (m *anchorManager) finishDownloaded(ctx context.Context, record localrepo.Record, rel string) error {
	if err := m.anchorFetchAuthority(record); err != nil {
		return err
	}
	batch, err := m.call(ctx, rel+"\n", "file-states", "--root", record.LocalPath)
	if err != nil {
		return err
	}
	if !batch.OK || len(batch.States) != 1 {
		return errors.New("download completion metadata unavailable")
	}
	state := batch.States[0]
	if !state.OK || state.Size == nil || state.Partial {
		return errors.New("download did not produce a fully resident file")
	}
	if state.Placeholder && !state.Local {
		identityPath, revision, size, ok := parseAnchorRemoteIdentity(state.Identity)
		if !ok || identityPath != rel || (size >= 0 && size != *state.Size) ||
			!anchorHydrationComplete(state, state.Identity, *state.Size) {
			return errors.New("download completion identity or residency changed")
		}
		if revision == 0 {
			revision, err = readAnchorSeedRevision(record.LocalPath)
			if err != nil {
				return err
			}
		}
		if err := m.adopt(ctx, record, state.Identity, revision); err != nil {
			return err
		}
		if err := m.waitDownloaded(ctx, record, rel); err != nil {
			return err
		}
		m.recordHydration(record, state.Identity, revision, state.Size)
		return nil
	}
	return m.waitDownloaded(ctx, record, rel)
}

func (m *anchorManager) waitDownloaded(ctx context.Context, record localrepo.Record, rel string) error {
	deadline, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	delay := 100 * time.Millisecond
	for {
		if err := deadline.Err(); err != nil {
			return err
		}
		repo := m.repos(record.ServerID, record.RepoID)
		if repo == nil {
			return ipcserver.ErrDepthUnavailable
		}
		summary := repo.Summary()
		if !summary.Attached || filepath.Clean(summary.LocalPath) != filepath.Clean(record.LocalPath) {
			return ipcserver.ErrDepthUnavailable
		}
		if repo.Snapshot().CommitRecoveryRequired {
			return ipcserver.ErrAnchorRecoveryRequired
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
		timer := time.NewTimer(delay)
		select {
		case <-deadline.Done():
			timer.Stop()
			return fmt.Errorf("downloaded file was not adopted into the working copy: %w", deadline.Err())
		case <-timer.C:
		}
		delay = min(delay*2, time.Second)
	}
}
