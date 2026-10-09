//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
)

func (m *anchorManager) bindExplorerRename(record localrepo.Record, bridge *anchorBridge, streams *anchorHelperStreams) {
	if !bridge.retainLocal || streams.providerPID == 0 {
		return
	}
	bridge.renameTree = func(ctx context.Context, cb anchorRenameCallback, physical func(context.Context) error) error {
		err := m.explorerTreeMove(ctx, record, streams.providerPID, cb, physical)
		if err != nil {
			m.log.Warnf("Explorer tree move refused or held; local data and pending evidence preserved: %v", err)
			if !commit.HasPendingRemoteTree(record.LocalPath) {
				return errors.Join(err, errAnchorRenameNotPrepared)
			}
		}
		return err
	}
}

func (m *anchorManager) explorerTreeMove(ctx context.Context, record localrepo.Record, provider uint32, cb anchorRenameCallback, physical func(context.Context) error) error {
	source, _, _, valid := parseAnchorRemoteIdentity(cb.Identity)
	volume := filepath.VolumeName(record.LocalPath)
	rootPath := strings.TrimPrefix(record.LocalPath, volume)
	prefix := rootPath + "\\"
	if !valid || len(volume) != 2 || cb.Flags != 7 || cb.ProcessID == 0 || cb.ProcessID == provider ||
		!strings.EqualFold(cb.NormalizedPath, strings.TrimPrefix(filepath.Join(record.LocalPath, filepath.FromSlash(source)), volume)) ||
		len(cb.TargetPath) <= len(prefix) || !strings.EqualFold(cb.TargetPath[:len(prefix)], prefix) {
		return errors.New("Explorer rename must stay inside the activated point")
	}
	destination := filepath.ToSlash(cb.TargetPath[len(prefix):])
	current, move, err := m.selectedMove(ctx, contract.RepoAnchorMoveSelection{ServerID: record.ServerID, RepoID: record.RepoID, Source: source, Destination: destination})
	if err != nil {
		return err
	}
	if current != record {
		return errors.New("Explorer rename attachment changed")
	}
	return m.moveTreeWithPhysical(ctx, record, move, physical, func(proof *anchorMoveObjects, _ *watcher.TreeMoveSnapshot) error {
		if proof.Tree.Root.Volume != cb.Volume || proof.Tree.Root.Index != cb.RootFileID {
			return errors.New("Explorer rename root binding differs")
		}
		for _, object := range proof.Objects {
			if object.Path == source && object.Directory && object.ID.Volume == cb.Volume && object.ID.Index == cb.FileID && object.RemoteIdentity == cb.Identity {
				return nil
			}
		}
		return errors.New("Explorer rename object binding differs")
	})
}
