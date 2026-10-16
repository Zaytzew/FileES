package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

var errCreationIntentEnded = errors.New("queued creation intent no longer active")

// Accept metadata-only retry without acquiring the shared execution fence.
// Never turn a duplicate click during a running attempt into another retry.
// Content checks happen in runOne after preparation admission, before SSH.
func (p *daemonProvisioner) queueInitialImportRetry(ctx context.Context, record localrepo.Record) (localrepo.Record, error) {
	p.mu.Lock()
	if err := ctx.Err(); err != nil {
		p.mu.Unlock()
		return record, err
	}
	current, ok := p.local.Get(record.OperationID)
	if !ok || current.State != localrepo.StateRepositoryCreated || current.RemoteDeletionObserved || current.RepoID != record.RepoID || current.ServerID != record.ServerID {
		p.mu.Unlock()
		return current, errCreationIntentEnded
	}
	if _, running := p.running[record.OperationID]; running {
		p.mu.Unlock()
		return current, nil
	}
	if p.provisioning == nil {
		p.mu.Unlock()
		return current, errors.New("repository provisioning journal is unavailable")
	}
	op, err := p.provisioning.Get(record.OperationID)
	if err == nil && (op.State == provisioning.StateAbandoned || op.RepoID != current.RepoID || op.RepoURL != current.RepoURL || filepath.Clean(op.LocalPath) != current.LocalPath) {
		err = errors.New("initial import no longer matches durable provisioning authority")
	}
	if err == nil {
		current, err = p.local.ResumeCreate(record.OperationID)
	}
	p.mu.Unlock()
	if err == nil {
		p.Enqueue(record.OperationID)
	}
	return current, err
}

// A full in-memory wakeup channel must not require a restart. Enqueue
// deduplicates already waiting IDs; concrete failures still need explicit retry.
func (p *daemonProvisioner) requeuePendingCreations() {
	for _, record := range p.local.List() {
		if record.LastError == "" && !record.RemoteDeletionObserved &&
			(record.State == localrepo.StateRequestPending || record.State == localrepo.StateRepositoryCreated) {
			if _, err := p.provisioning.Get(record.OperationID); err == nil {
				p.Enqueue(record.OperationID)
			}
		}
	}
}

// Waiting happens AFTER the durable intent has been acknowledged by IPC.
// Only preparation admission waits here: no remote command is retried and
// no long transfer holds this global mutex. Shutdown leaves the stored intent
// untouched, so the usual provisioning restore resumes the same operation.
func (p *daemonProvisioner) waitForCreationTurn(ctx context.Context) (func(), error) {
	p.creationWaiters.Add(1)
	defer p.creationWaiters.Add(-1)
	retry := time.NewTicker(25 * time.Millisecond)
	defer retry.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.localIntentMu.TryLock() {
			if err := ctx.Err(); err != nil {
				p.localIntentMu.Unlock()
				return nil, err
			}
			return p.localIntentMu.Unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-retry.C:
		}
	}
}

// Admission-time picker checks are not execution authority. Read the current
// record, activation and target again under the local fence before even
// preparing the private carrier, then release before copy/network/checkout.
func (p *daemonProvisioner) prepareQueuedCreation(ctx context.Context, id string) (localrepo.Record, clientprofile.Profile, error) {
	leave, err := p.waitForCreationTurn(ctx)
	if err != nil {
		return localrepo.Record{}, clientprofile.Profile{}, err
	}
	defer leave()
	record, ok := p.local.Get(id)
	if !ok || record.RemoteDeletionObserved || record.LastError != "" || (record.State != localrepo.StateRequestPending && record.State != localrepo.StateRepositoryCreated) {
		return record, clientprofile.Profile{}, errCreationIntentEnded
	}
	profile, ok := p.Profile(record.ServerID)
	if !ok {
		return record, profile, errors.New("activated client profile is unavailable")
	}
	op, err := p.provisioning.Get(id)
	if err != nil {
		return record, profile, err
	}
	if op.ClientID != profile.ClientID {
		return record, profile, errors.New("queued creation belongs to another activation")
	}
	mode := provisioning.LocalPathCreate
	if op.RepoID != "" {
		mode = provisioning.LocalPathCreateResume
	}
	if op.Archive != nil {
		if op.Archive.Target != record.LocalPath {
			return record, profile, errors.New("archive target does not match durable local intent")
		}
		// The final checkout may already exist after a lost reply. Its UUID
		// and clean status are checked by checkoutArchiveTarget, not here.
		if op.Archive.LoadedUUID != "" {
			return record, profile, ctx.Err()
		}
		mode = provisioning.LocalPathAttach
	} else if filepath.Clean(op.LocalPath) != record.LocalPath {
		return record, profile, errors.New("creation target does not match durable local intent")
	}
	check, err := provisioning.PreflightLocalPath(record.LocalPath, mode, p.otherRoots(id))
	if err != nil {
		return record, profile, err
	}
	if check.CanonicalPath != record.LocalPath {
		return record, profile, errors.New("queued creation target location changed")
	}
	return record, profile, ctx.Err()
}
