//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

type anchorBatchAdapter struct{ manager *anchorManager }

// HeadAvailability is a bounded, read-only projection. No SVN network read,
// hydration, adoption or release is performed while inspecting these badges.
func (a anchorBatchAdapter) HeadAvailability(ctx context.Context, serverID, repoID, dir string,
	entries []contract.RepoHeadEntry) (map[string]contract.RepoHeadAvailability, bool, error) {
	m := a.manager
	if m.lifecycle == nil {
		return nil, false, nil
	}
	for _, record := range m.lifecycle.List() {
		if record.ServerID != serverID || record.RepoID != repoID || !record.Anchor || record.State != localrepo.StateAttached {
			continue
		}
		if err := m.currentReleaseRecord(record); err != nil {
			return nil, true, err
		}
		guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
		if err != nil {
			return nil, true, err
		}
		defer closeAnchorReleaseGuards(guards)
		var root windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(guards[len(guards)-1], &root); err != nil {
			return nil, true, err
		}
		tree, err := loadAnchorRemoteTree(record, anchorReleaseID(root))
		if err != nil {
			return nil, true, err
		}
		// Never label a folder local from a manifest older than its HEAD entry.
		known := make([]contract.RepoHeadEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.Kind == "dir" && entry.Revision <= tree.Revision {
				known = append(known, entry)
			}
		}
		states, err := anchorFolderAvailability(ctx, tree.Nodes, dir, known, func(paths []string) ([]cfapiAnswer, error) {
			answer, err := m.call(ctx, strings.Join(paths, "\n")+"\n", "file-states", "--root", record.LocalPath)
			if err == nil && !answer.OK {
				err = errors.New("availability metadata inspection failed")
			}
			return answer.States, err
		})
		if err == nil {
			err = m.currentReleaseRecord(record)
		}
		return states, true, err
	}
	return nil, false, nil
}

func (a anchorBatchAdapter) record(p contract.RepoAnchorPlanPayload) (localrepo.Record, error) {
	m := a.manager
	if m.lifecycle == nil {
		return localrepo.Record{}, ipcserver.ErrDepthUnavailable
	}
	for _, record := range m.lifecycle.List() {
		if record.ServerID == p.ServerID && record.RepoID == p.RepoID && record.Anchor && record.State == localrepo.StateAttached {
			if err := m.currentReleaseRecord(record); err != nil {
				return localrepo.Record{}, err
			}
			if commit.HasPendingLocalMutation(record.LocalPath) {
				return localrepo.Record{}, commit.LocalMutationPendingError(record.LocalPath)
			}
			repo := m.repos(record.ServerID, record.RepoID)
			if repo == nil {
				return localrepo.Record{}, ipcserver.ErrDepthUnavailable
			}
			status := repo.Snapshot()
			if status.CommitRecoveryRequired {
				return localrepo.Record{}, ipcserver.ErrAnchorRecoveryRequired
			}
			if status.PublishProgress != nil {
				return localrepo.Record{}, commit.ErrLocalStatusBusy
			}
			return record, nil
		}
	}
	return localrepo.Record{}, ipcserver.ErrDepthUnavailable
}

func (a anchorBatchAdapter) selection(ctx context.Context, p contract.RepoAnchorPlanPayload, record localrepo.Record) (anchorBatchSelection, error) {
	m := a.manager
	guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
	if err != nil {
		return anchorBatchSelection{}, err
	}
	defer closeAnchorReleaseGuards(guards)
	var root windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(guards[len(guards)-1], &root); err != nil {
		return anchorBatchSelection{}, err
	}
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return anchorBatchSelection{}, err
	}
	reader, ok := svn.(anchorTreeReader)
	if !ok {
		return anchorBatchSelection{}, ipcserver.ErrDepthUnavailable
	}
	revision, err := reader.Revision(ctx, record.RepoURL)
	if err != nil {
		return anchorBatchSelection{}, err
	}
	dir, err := os.MkdirTemp("", "filees-anchor-batch-*")
	if err != nil {
		return anchorBatchSelection{}, err
	}
	defer os.RemoveAll(dir)
	manifest := filepath.Join(dir, "tree.jsonl")
	if _, err := reader.HistoryListTree(ctx, record.RepoURL, revision, manifest); err != nil {
		return anchorBatchSelection{}, err
	}
	binding, _ := json.Marshal(struct {
		Operation, Server, Repo, URL, Path string
		Root                               anchorReleaseFileID
	}{
		record.OperationID, record.ServerID, record.RepoID, record.RepoURL, record.LocalPath, anchorReleaseID(root),
	})
	selection, err := anchorBatchPlan(ctx, manifest, p.Path, p.Action, string(binding), func(paths []string) ([]cfapiAnswer, error) {
		answer, err := m.call(ctx, strings.Join(paths, "\n")+"\n", "file-states", "--root", record.LocalPath)
		if err != nil {
			return nil, err
		}
		if !answer.OK {
			return nil, errors.New("folder metadata inspection failed")
		}
		return answer.States, nil
	})
	selection.revision = revision
	// The exact execution still includes HEAD and all metadata. Only the
	// read-only download consent can survive an unrelated revision/USN change.
	exact := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", selection.preview.Fingerprint, revision)))
	selection.preview.Fingerprint = hex.EncodeToString(exact[:])
	return selection, err
}

func (a anchorBatchAdapter) Plan(ctx context.Context, p contract.RepoAnchorPlanPayload) (contract.RepoAnchorPlan, error) {
	record, err := a.record(p)
	if err != nil {
		return contract.RepoAnchorPlan{}, err
	}
	selection, err := a.selection(ctx, p, record)
	return selection.preview, err
}

func (a anchorBatchAdapter) Execute(ctx context.Context, p contract.RepoAnchorBatchPayload) (contract.RepoAnchorBatchResult, error) {
	var result contract.RepoAnchorBatchResult
	record, err := a.record(p.RepoAnchorPlanPayload)
	if err != nil {
		return result, err
	}
	guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
	if err != nil {
		return result, err
	}
	defer closeAnchorReleaseGuards(guards)
	selection, err := a.selection(ctx, p.RepoAnchorPlanPayload, record)
	if err != nil {
		return result, err
	}
	if !selection.preview.Directory || p.Fingerprint != selection.preview.Fingerprint {
		return result, ipcserver.ErrAnchorSelectionChanged
	}
	result = contract.RepoAnchorBatchResult{LocalPath: record.LocalPath, Path: p.Path, State: "attached", Total: selection.preview.Files}
	result.Completed, err = runAnchorBatch(ctx, selection, func(ctx context.Context, file anchorBatchFile) error {
		if err := a.manager.currentReleaseRecord(record); err != nil {
			return err
		}
		if p.Action == "release" {
			if err := a.checkFile(ctx, record, file); err != nil {
				return err
			}
			return a.manager.releaseFile(ctx, record, file.rel)
		}
		return a.download(ctx, record, file, selection.revision)
	})
	if err != nil {
		// Once a file action has started, zero confirmations do not prove zero
		// effects (hydration can precede adoption). Never return a retryable
		// pre-effect refusal from this phase, even for the first file.
		a.manager.log.Warnf("Explorer folder action interrupted: %v", err)
		result.State = "partial"
		result.FailureCode, result.FailureKey, _ = ipcserver.AnchorBatchFailure(err, p.Action)
	}
	return result, nil
}

func (a anchorBatchAdapter) download(ctx context.Context, record localrepo.Record, file anchorBatchFile, revision int64) error {
	m := a.manager
	if commit.HasPendingLocalMutation(record.LocalPath) {
		return commit.LocalMutationPendingError(record.LocalPath)
	}
	m.mu.Lock()
	connection := m.running[anchorKey(record)]
	m.mu.Unlock()
	if connection.bridge == nil || connection.ctx == nil || connection.ctx.Err() != nil {
		return ipcserver.ErrDepthUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(connection.ctx, cancel)
	defer func() { stop(); cancel() }()
	// A preview is not authority to touch a later file or a new attachment.
	if err := a.checkFile(ctx, record, file); err != nil {
		return err
	}
	// CFAPI identifies the originating executable with an NT device path.
	// Resolve the opened helper, not its basename, and prevent its replacement
	// while this one-path approval is live.
	imageName, err := windows.UTF16PtrFromString(m.helper)
	if err != nil {
		return err
	}
	image, err := windows.CreateFile(imageName, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(image)
	var name [32768]uint16
	n, err := windows.GetFinalPathNameByHandle(image, &name[0], uint32(len(name)), 2 /* VOLUME_NAME_NT */)
	if err != nil || n == 0 || n >= uint32(len(name)) {
		return errors.New("cannot establish explicit download helper identity")
	}
	withdraw, err := connection.bridge.guard.authorizePinned(windows.UTF16ToString(name[:n]), file.rel, revision, *file.state.Size)
	if err != nil {
		return err
	}
	defer withdraw()
	return m.downloadFile(ctx, record, file.rel)
}

func (a anchorBatchAdapter) checkFile(ctx context.Context, record localrepo.Record, file anchorBatchFile) error {
	answer, err := a.manager.call(ctx, "", "file-state", "--root", record.LocalPath, "--rel", file.rel)
	if err != nil {
		return err
	}
	if !sameAnchorReleaseState(answer, file.state) || answer.Identity != file.state.Identity ||
		answer.Size == nil || file.state.Size == nil || *answer.Size != *file.state.Size {
		return fmt.Errorf("selected file changed before folder action")
	}
	return nil
}
