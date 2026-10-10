//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"time"

	"filees/pkg/anchorqueue"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

// Advertised only when the private durable store successfully opened.
type anchorQueuedBatchAdapter struct{ anchorBatchAdapter }

func (a anchorQueuedBatchAdapter) QueuedReleases() []contract.RepoAnchorQueueResult {
	var results []contract.RepoAnchorQueueResult
	fault := a.manager.releaseQueue.Fault()
	for _, x := range a.manager.releaseQueue.List() {
		if fault != "" && (x.Result.State == "queued" || x.Result.State == "running") {
			x.Result.State, x.Result.FailureCode, x.Result.FailureKey = "failed", "HEAD-2025", "head.release_queue_interrupted"
			x.Result.UpdatedAt = fault
			x.Result.FailureDetail = "release queue persistence failed; retained disk evidence must be inspected before replay"
		}
		results = append(results, x.Result)
	}
	return results
}

func (a anchorQueuedBatchAdapter) Queue(ctx context.Context, p contract.RepoAnchorQueuePayload) (contract.RepoAnchorQueueResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.RepoAnchorQueueResult{}, err
	}
	// A repeated IPC submission reads its original outcome even if the folder
	// subsequently moved. It never creates an intention for the new location.
	for _, x := range a.manager.releaseQueue.List() {
		if x.Payload.IntentID == p.IntentID {
			if x.Payload != p {
				return contract.RepoAnchorQueueResult{}, anchorqueue.ErrConflict
			}
			return x.Result, nil
		}
	}
	for _, record := range a.manager.lifecycle.List() {
		if record.ServerID != p.ServerID || record.RepoID != p.RepoID || !record.Anchor || record.State != localrepo.StateAttached {
			continue
		}
		if err := a.manager.currentReleaseRecord(record); err != nil {
			return contract.RepoAnchorQueueResult{}, err
		}
		snapshot, ok := a.manager.releasePlans.get(p, record.OperationID, record.RepoURL, record.LocalPath)
		if !ok {
			return contract.RepoAnchorQueueResult{}, ipcserver.ErrAnchorSelectionChanged
		}
		// Membership comes from the daemon's confirmed preview, not a refreshed
		// folder or the caller. Versions/USN will be checked afresh on execution.
		x, err := a.manager.releaseQueue.EnqueueSnapshot(p, record.OperationID, record.RepoURL, record.LocalPath, snapshot)
		return x.Result, err
	}
	return contract.RepoAnchorQueueResult{}, ipcserver.ErrDepthUnavailable
}

func queueFailure(x anchorqueue.Intent, err error) contract.RepoAnchorBatchResult {
	code, key, _ := ipcserver.AnchorBatchFailure(err, "release")
	return contract.RepoAnchorBatchResult{State: "failed", LocalPath: x.Root, Path: x.Payload.Path, Total: x.Payload.Files, FailureCode: code, FailureKey: key, FailureDetail: anchorqueue.Diagnostic(err)}
}

func (m *anchorManager) queuedReleaseReady(x anchorqueue.Intent) (bool, error) {
	r, ok := m.lifecycle.Get(x.OperationID)
	if !ok || r.State != localrepo.StateAttached || !r.Anchor || r.ServerID != x.Payload.ServerID || r.RepoID != x.Payload.RepoID || r.RepoURL != x.RepoURL || r.LocalPath != x.Root {
		return false, m.releaseQueue.Set(x.Payload.IntentID, "queued", queueFailure(x, ipcserver.ErrAnchorSelectionChanged))
	}
	// The connection is rebuilt during ordinary startup. Do not turn that
	// interval into failure or let it authorise an old provider's attachment.
	m.mu.Lock()
	c := m.running[anchorKey(r)]
	ready := c.bridge != nil && c.root == r.LocalPath && c.ctx != nil && c.ctx.Err() == nil
	m.mu.Unlock()
	return ready, nil
}

func (m *anchorManager) tickReleaseQueue(ctx context.Context) {
	if m.releaseQueue == nil || ctx.Err() != nil || !m.releaseQueueActive.CompareAndSwap(false, true) {
		return
	}
	m.releaseQueueJobs.Add(1)
	go func() {
		defer m.releaseQueueJobs.Done()
		defer m.releaseQueueActive.Store(false)
		before := map[string]contract.RepoAnchorQueueResult{}
		for _, x := range m.releaseQueue.List() {
			before[x.Payload.IntentID] = x.Result
		}
		jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		err := m.releaseRunner.Tick(jobCtx, m.releaseQueue, commit.ErrLocalStatusBusy, m.queuedReleaseReady,
			func(ctx context.Context, x anchorqueue.Intent) (contract.RepoAnchorBatchResult, error) {
				r, ok := m.lifecycle.Get(x.OperationID)
				if !ok || r.LocalPath != x.Root || r.RepoURL != x.RepoURL || r.State != localrepo.StateAttached {
					return queueFailure(x, ipcserver.ErrAnchorSelectionChanged), nil
				}
				result, err := (anchorBatchAdapter{manager: m}).executeSnapshot(ctx, x.Payload.RepoAnchorBatchPayload, x.Payload.Files, x.Snapshot)
				if errors.Is(err, commit.ErrLocalStatusBusy) {
					return result, err
				}
				if err != nil {
					return queueFailure(x, err), nil
				}
				return result, nil
			})
		if err != nil && ctx.Err() == nil {
			if err.Error() != m.releaseQueueLastError {
				m.log.Warnf("Explorer release queue stopped: %v", err)
			}
			m.releaseQueueLastError = err.Error()
		} else if err == nil {
			m.releaseQueueLastError = ""
		}
		if m.guardChanged != nil {
			changed := map[string]bool{}
			for _, x := range m.releaseQueue.List() {
				if x.Result != before[x.Payload.IntentID] {
					changed[x.Payload.RepoID] = true
				}
			}
			for id := range changed {
				m.guardChanged(id)
			}
		}
	}()
}
