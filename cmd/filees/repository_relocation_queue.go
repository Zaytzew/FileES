package main

import (
	"context"
	"errors"
	"sort"

	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

// Wakeups are disposable: the lifecycle journal owns the accepted move.
// Recover a full channel and a daemon restart without asking the person again.
func (p *daemonProvisioner) requeuePendingRelocations() {
	records := p.local.List()
	sort.SliceStable(records, func(i, j int) bool { return records[i].UpdatedAt.Before(records[j].UpdatedAt) })
	for _, record := range records {
		if record.RelocationQueued && !record.RemoteDeletionObserved &&
			(record.State == localrepo.StateAttached || record.State == localrepo.StateRelocating) {
			p.Enqueue(record.OperationID)
		}
	}
}

func (p *daemonProvisioner) runQueuedRelocation(ctx context.Context, id string) {
	// Reuse creation admission: metadata ticks yield while an explicit local
	// intent waits. No command/commit is retried by this wait.
	leave, err := p.waitForCreationTurn(ctx)
	if err != nil {
		return
	}
	defer leave()
	record, ok := p.local.Get(id)
	if !ok || !record.RelocationQueued || record.RemoteDeletionObserved ||
		(record.State != localrepo.StateAttached && record.State != localrepo.StateRelocating) {
		return
	}
	profile, ok := p.Profile(record.ServerID)
	if !ok {
		return
	} // activation unavailable: retain, never use old credentials
	if p.relocationCheck != nil {
		if err := p.relocationCheck(record); err != nil {
			// An unstarted move may be refused safely. An executing receipt
			// may already be post-rename: retain both paths for verification.
			if record.State == localrepo.StateAttached {
				_, _ = p.local.FailRelocation(id, err)
			}
			return
		}
	}
	if commit.HasPendingLocalMutation(record.LocalPath) {
		return
	}
	if record.State == localrepo.StateAttached {
		record, err = p.local.StartQueuedRelocation(record)
		if err != nil {
			return
		}
	}
	if ctx.Err() == nil {
		p.runRelocate(ctx, record, profile)
	}
}

// Quiesce may have waited for a writer which created durable recovery proof.
// Let that old runtime finish it; the pending destination remains reserved.
func (p *daemonProvisioner) deferQueuedRelocation(ctx context.Context, record localrepo.Record, cause error) bool {
	if !record.RelocationQueued {
		return false
	}
	if ctx.Err() != nil {
		return true
	} // shutdown: keep the forward receipt
	if !errors.Is(cause, commit.ErrLocalStatusBusy) && !commit.HasPendingLocalMutation(record.LocalPath) {
		return false
	}
	updated, err := p.local.DeferQueuedRelocation(record)
	if err != nil {
		talk.With("relocation:"+record.OperationID).Warnf("retain queued move: %v", err)
		return true
	}
	if profile, ok := p.Profile(updated.ServerID); ok {
		p.publishLocalRecord(ctx, updated, profile)
	}
	if p.anchorChanged != nil {
		p.anchorChanged()
	}
	return true
}
