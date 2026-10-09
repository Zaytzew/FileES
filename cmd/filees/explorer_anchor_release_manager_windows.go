//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func anchorReleaseLocation(r localrepo.Record) anchorReleaseIdentity {
	return anchorReleaseIdentity{ServerID: r.ServerID, RepoID: r.RepoID, RepoURL: r.RepoURL, WC: r.LocalPath}
}

func (m *anchorManager) wireRelease(ipc *ipcserver.Server) {
	ipc.SetAnchorRelease(m.release)
	ipc.SetAnchorBatch(anchorBatchAdapter{manager: m})
	m.wireTreeMove(ipc)
}

func (m *anchorManager) enterReleaseIntent() (func(), error) {
	if m.localIntentMu == nil {
		return func() {}, nil
	}
	if !m.localIntentMu.TryLock() {
		return nil, commit.ErrLocalStatusBusy
	}
	return m.localIntentMu.Unlock, nil
}

func (m *anchorManager) currentReleaseRecord(record localrepo.Record) error {
	if m.lifecycle == nil || m.repos == nil {
		return ipcserver.ErrDepthUnavailable
	}
	current, exists := m.lifecycle.Get(record.OperationID)
	if !exists || current.State != localrepo.StateAttached || !current.Anchor ||
		current.ServerID != record.ServerID || current.RepoID != record.RepoID ||
		current.RepoURL != record.RepoURL || current.LocalPath != record.LocalPath {
		return ipcserver.ErrDepthUnavailable
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return ipcserver.ErrDepthUnavailable
	}
	summary := repo.Summary()
	if !summary.Attached || summary.LocalPath != record.LocalPath || summary.URL != record.RepoURL ||
		(summary.Access != "r" && summary.Access != "rw") {
		return ipcserver.ErrDepthUnavailable
	}
	return nil
}

func (m *anchorManager) release(ctx context.Context, serverID, repoID, rel string) error {
	if m.lifecycle == nil {
		return ipcserver.ErrDepthUnavailable
	}
	for _, record := range m.lifecycle.List() {
		if record.ServerID == serverID && record.RepoID == repoID && record.Anchor && record.State == localrepo.StateAttached {
			return m.releaseFile(ctx, record, rel)
		}
	}
	return ipcserver.ErrDepthUnavailable
}

func (m *anchorManager) releaseStore() (string, error) {
	// Existing private FileES data directory, outside every admitted WC.
	root := filepath.Dir(m.profiles)
	guards, err := guardAnchorReleaseDirectories(root, "")
	if err != nil {
		return "", err
	}
	defer closeAnchorReleaseGuards(guards)
	store := filepath.Join(root, "explorer-release")
	if err := os.Mkdir(store, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	checked, err := guardAnchorReleaseDirectories(store, "")
	if err != nil {
		return "", err
	}
	closeAnchorReleaseGuards(checked)
	return store, nil
}

func (m *anchorManager) releaseIO(record localrepo.Record, bridge *anchorBridge) (anchorReleaseIO, anchorReleaseRemoteReader, error) {
	var ops anchorReleaseIO
	if bridge == nil || !bridge.retainLocal || bridge.root != record.LocalPath {
		return ops, nil, errors.New("release requires the active retained FileES provider")
	}
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return ops, nil, err
	}
	excluder, ok := svn.(anchorReleaseExcluder)
	if !ok {
		return ops, nil, errors.New("conditional file release is unavailable")
	}
	remote, ok := svn.(anchorReleaseRemoteReader)
	if !ok {
		return ops, nil, errors.New("independent release verification is unavailable")
	}
	ops = anchorReleaseIO{helper: m.helper, svn: excluder,
		inspect: func(ctx context.Context, rel string) (cfapiAnswer, error) {
			return m.call(ctx, "", "file-state", "--root", record.LocalPath, "--rel", rel)
		},
		prepare: func(ctx context.Context, rel, usn string, id anchorReleaseFileID) error {
			return bridge.prepareRelease(ctx, rel, usn, strconv.FormatUint(uint64(id.Volume), 10)+":"+strconv.FormatUint(id.Index, 10))
		},
		restore: func(ctx context.Context, rel string, size int64) error {
			base, err := excluder.InspectPathExclusion(ctx, record.LocalPath, rel)
			if err != nil || !base.Excluded || !base.Idle || base.Revision <= 0 {
				return errors.Join(err, errors.New("restoration revision unavailable"))
			}
			return bridge.restoreReleasePinned(ctx, rel, base.Revision, size)
		},
		remoteReceipt: func(ctx context.Context, r anchorReleaseRecord) error {
			return m.noteReleasedRemote(ctx, record, r)
		},
	}
	return ops, remote, nil
}

// Missing retained bytes are admitted only through an already durable exact
// retirement intent. This closes a crash after delete/before receipt, not an
// alternate recovery path for missing/corrupt evidence or an occupied target.
func advanceManagedAnchorRelease(ctx context.Context, j *anchorReleaseJournal, ops anchorReleaseIO, remote anchorReleaseRemoteReader) error {
	_, err := os.Lstat(filepath.Join(j.store, j.id, "data.bin"))
	if !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		result, err := j.advanceRelease(ctx, ops)
		if err != nil {
			return err
		}
		if !result.seedObserved {
			return errors.New("released remote seed is not confirmed")
		}
		return nil
	}
	r, err := j.inspectRecords(ctx)
	if err != nil {
		return err
	}
	if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
		return err
	}
	if exists, err := observeAnchorReleaseSeed(ctx, r, ops.inspect); err != nil || !exists {
		return errors.Join(err, errors.New("retired release seed is not confirmed"))
	}
	return j.retireCopy(ctx, anchorReleaseRecoveryIO{svn: ops.svn, inspect: ops.inspect, restore: ops.restore}, remote)
}

func finishManagedAnchorRelease(ctx context.Context, j *anchorReleaseJournal, fence *anchorReleaseFence, ops anchorReleaseIO, remote anchorReleaseRemoteReader) error {
	if err := j.retireCopy(ctx, anchorReleaseRecoveryIO{svn: ops.svn, inspect: ops.inspect, restore: ops.restore}, remote); err != nil {
		return err
	}
	// Check the final local state even when retirement was already complete.
	r, err := j.inspectRecords(ctx)
	if err != nil {
		return err
	}
	if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
		return err
	}
	if exists, err := observeAnchorReleaseSeed(ctx, r, ops.inspect); err != nil || !exists {
		return errors.Join(err, errors.New("completed release seed changed"))
	}
	if ops.remoteReceipt != nil {
		if err := ops.remoteReceipt(ctx, r); err != nil {
			return err
		}
	}
	return j.clearFence(ctx, fence)
}

func (m *anchorManager) recoverAnchorRelease(ctx context.Context, record localrepo.Record, bridge *anchorBridge) {
	previous := ""
	pendingBefore := commit.HasPendingFileRelease(record.LocalPath)
	for ctx.Err() == nil {
		var err error
		if commit.HasPendingFileRelease(record.LocalPath) {
			err = m.resumeAnchorRelease(ctx, record, bridge)
		}
		if ctx.Err() != nil {
			return
		}
		pendingNow := commit.HasPendingFileRelease(record.LocalPath)
		if pendingNow != pendingBefore && m.guardChanged != nil {
			m.guardChanged(record.RepoID)
		}
		pendingBefore = pendingNow
		if err != nil && commit.HasPendingFileRelease(record.LocalPath) && err.Error() != previous {
			previous = err.Error()
			m.log.Warnf("Explorer release recovery remains fenced: %v", err)
		}
		if err == nil {
			previous = ""
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *anchorManager) resumeAnchorRelease(ctx context.Context, record localrepo.Record, bridge *anchorBridge) error {
	leave, err := m.enterReleaseIntent()
	if err != nil {
		return err
	}
	defer leave()
	if err := m.currentReleaseRecord(record); err != nil {
		return err
	}
	store, err := m.releaseStore()
	if err != nil {
		return err
	}
	j, fence, err := loadAnchorReleaseFence(ctx, store, anchorReleaseLocation(record))
	if err != nil {
		return err
	}
	ops, remote, err := m.releaseIO(record, bridge)
	if err != nil {
		return err
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return ipcserver.ErrDepthUnavailable
	}
	return repo.WithFileRelease(ctx, j.identity.Rel, true, func() error {
		// Reload under the actual writer guard; the earlier read grants nothing.
		var err error
		j, fence, err = loadAnchorReleaseFence(ctx, store, anchorReleaseLocation(record))
		if err != nil {
			return err
		}
		return advanceManagedAnchorRelease(ctx, j, ops, remote)
	}, func() error { return finishManagedAnchorRelease(ctx, j, fence, ops, remote) })
}

// Explicit single-file action. No ordinary SVN fallback.
func (m *anchorManager) releaseFile(ctx context.Context, record localrepo.Record, rel string) error {
	if _, ok := cleanAnchorIdentity(rel); !ok {
		return errors.New("select one published file for release")
	}
	leave, err := m.enterReleaseIntent()
	if err != nil {
		return err
	}
	defer leave()
	if err := m.currentReleaseRecord(record); err != nil {
		return err
	}
	m.mu.Lock()
	connection := m.running[anchorKey(record)]
	bridge := connection.bridge
	m.mu.Unlock()
	if connection.ctx == nil || connection.ctx.Err() != nil {
		return ipcserver.ErrDepthUnavailable
	}
	// A detached/stopped provider may not finish a user action in a later
	// attachment. Preserve its durable evidence if cancellation interrupts it.
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(connection.ctx, cancel)
	defer func() { stop(); cancel() }()
	ops, remote, err := m.releaseIO(record, bridge)
	if err != nil {
		return err
	}
	store, err := m.releaseStore()
	if err != nil {
		return err
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return ipcserver.ErrDepthUnavailable
	}
	var j *anchorReleaseJournal
	var fence *anchorReleaseFence
	ops.retained = func(ctx context.Context, saved *anchorReleaseJournal) error {
		j = saved
		var err error
		fence, err = saved.persistFence(ctx)
		if err == nil && m.guardChanged != nil {
			m.guardChanged(record.RepoID)
		}
		return err
	}
	return repo.WithFileRelease(ctx, rel, false, func() error {
		if err := m.currentReleaseRecord(record); err != nil {
			return err
		}
		identity := anchorReleaseLocation(record)
		identity.Rel = rel
		base, err := ops.svn.InspectPathExclusion(ctx, record.LocalPath, rel)
		if err != nil {
			return err
		}
		identity.RepoUUID, identity.Revision = base.RepositoryUUID, base.Revision
		var result anchorReleaseRecovery
		j, result, err = startAnchorRelease(ctx, store, identity, ops)
		if err != nil {
			return err
		}
		if !result.seedObserved {
			return fmt.Errorf("released path is not a confirmed remote seed")
		}
		return nil
	}, func() error { return finishManagedAnchorRelease(ctx, j, fence, ops, remote) })
}
