//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

// Only cursors survive a turn, not a second copy of every repository tree.
type anchorDirectoryCursor struct{ after string }

func (m *anchorManager) refreshDirectoryStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge, cursor *anchorDirectoryCursor) error {
	unlock, err := m.enterReleaseIntent()
	if err != nil {
		return err
	}
	defer unlock()
	if err = m.currentReleaseRecord(record); err != nil {
		return err
	}
	if commit.HasPendingLocalMutation(record.LocalPath) {
		return commit.LocalMutationPendingError(record.LocalPath)
	}
	guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	var root windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(guards[len(guards)-1], &root); err != nil {
		return err
	}
	tree, err := loadAnchorRemoteTree(record, anchorReleaseID(root))
	if err != nil {
		return err
	}
	dirs, err := anchorDirectoryOrder(tree.Nodes)
	if err != nil {
		return err
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return errors.New("directory writer unavailable")
	}
	start := 0
	for i, dir := range dirs {
		if dir == cursor.after {
			start = i + 1
			break
		}
	}
	until := time.Now().Add(500 * time.Millisecond)
	for index := start; index < len(dirs) && index < start+8; index++ {
		dir := dirs[index]
		// This cursor records attempts, not successful confirmations. A stale
		// or unreadable directory must not starve siblings; a later cycle retries
		// it with fresh evidence and no inferred acknowledgement.
		cursor.after = dir
		nodes, selectionErr := anchorDirectorySelection(tree.Nodes, dir)
		// Oversized trees remain pending. In particular, do not leave an old
		// clean marker in place just because this turn cannot prove all children.
		oversized := selectionErr != nil
		paths := []string{dir}
		for _, n := range nodes {
			paths = append(paths, n.Path)
		}
		states := make(map[string]cfapiAnswer, len(paths))
		turn, cancel := context.WithTimeout(ctx, 2*time.Second)
		a, e := m.call(turn, strings.Join(paths, "\n")+"\n", "tree-states", "--root", record.LocalPath)
		cancel()
		if e != nil || !a.OK || len(a.States) != len(paths) {
			return errors.New("directory metadata unavailable")
		}
		for i, s := range a.States {
			states[paths[i]] = s
		}
		parent := states[dir]
		if !parent.OK || !parent.Directory {
			return errors.New("directory metadata unavailable")
		}
		if dir != "." {
			found := false
			for _, n := range tree.Nodes {
				if n.Path == dir {
					found = anchorDirectoryStateMatches(n, parent)
					break
				}
			}
			if !found {
				return errors.New("directory manifest identity changed")
			}
		}
		clean := !oversized
		for _, n := range nodes {
			if !anchorDirectoryStateMatches(n, states[n.Path]) || !states[n.Path].InSync {
				clean = false
				break
			}
		}
		// Observation and writer/ACK have separate bounded deadlines. No WC
		// writer is held during metadata reads; the native proof rechecks USNs.
		turn, cancel = context.WithTimeout(ctx, 2*time.Second)
		confirm := func(rows []client.StatusEntry) error {
			clean = clean && anchorDirectorySVNClean(record.LocalPath, dir, rows, states)
			// Even an already-green parent needs the helper's complete physical
			// listing proof: SVN can omit children below an unversioned folder.
			if !clean && !parent.InSync {
				return nil
			}
			if e := m.currentReleaseRecord(record); e != nil {
				return e
			}
			return bridge.setDirectoryState(turn, dir, parent, clean, nodes, states)
		}
		if oversized {
			// Clearing only the parent needs no descendant publication proof,
			// but must still share the real WC writer guard and lifecycle fence.
			err = repo.WithLocalMetadataGuard(turn, func() error { return confirm(nil) })
		} else {
			err = repo.WithLocalSubtreeStatus(turn, dir, confirm)
		}
		cancel()
		if err != nil {
			return err
		}
		if !time.Now().Before(until) {
			return nil
		}
	}
	if cursor.after == "." {
		cursor.after = ""
	}
	return ctx.Err()
}
