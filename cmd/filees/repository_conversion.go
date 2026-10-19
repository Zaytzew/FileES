package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"filees/pkg/clientprofile"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"github.com/google/uuid"
)

// No producer/GUI command is enabled until this complete native adapter is
// installed. Finalize pins the terminal root until its lifecycle CAS commits;
// it does not perform cleanup or infer identity from directory existence.
type conversionEffectsAdapter interface {
	localrepo.ConversionEffects
	Finalize(context.Context, localrepo.ConversionRecord) (release func(), err error)
}

func (p *daemonProvisioner) conversionJournal(r localrepo.Record, create bool) (*localrepo.ConversionJournal, error) {
	p.conversionMu.Lock()
	defer p.conversionMu.Unlock()
	if !filepath.IsAbs(p.conversionRoot) || uuid.Validate(r.OperationID) != nil {
		return nil, errors.New("conversion needs its private absolute journal store")
	}
	if j := p.conversionJournals[r.OperationID]; j != nil {
		return j, nil
	}
	path := filepath.Join(p.conversionRoot, r.OperationID+".json")
	j, err := localrepo.OpenConversionJournal(path)
	if errors.Is(err, os.ErrNotExist) && create && r.ConversionID == "" {
		j, err = localrepo.CreateConversionJournal(path, r)
	}
	if err != nil {
		return nil, err
	}
	if p.conversionJournals == nil {
		p.conversionJournals = map[string]*localrepo.ConversionJournal{}
	}
	p.conversionJournals[r.OperationID] = j
	return j, nil
}

func (p *daemonProvisioner) queueConversion(ctx context.Context, serverID, repoID string) (localrepo.Record, error) {
	if err := ctx.Err(); err != nil {
		return localrepo.Record{}, err
	}
	if p.newConversionEffects == nil || p.anchorEnabled == nil || !p.anchorEnabled(serverID) || p.relocationCheck == nil {
		return localrepo.Record{}, errors.New("working-copy conversion is not available on this build")
	}
	r, err := p.local.CurrentLifecycle(serverID, repoID)
	if err != nil {
		return r, err
	}
	if _, ok := p.Profile(serverID); !ok {
		return r, errors.New("conversion activation is unavailable")
	}
	if err := p.relocationCheck(r); err != nil {
		return r, err
	}
	if r.ConversionID != "" {
		p.Enqueue(r.OperationID)
		return r, nil
	} // idempotent, not an implicit Retry
	if r.State != localrepo.StateAttached || r.Anchor || r.Sparse || r.Purpose != "" || r.RelocationQueued {
		return r, errors.New("conversion requires an ordinary full working copy")
	}
	check, err := provisioning.PreflightLocalPath(r.LocalPath, provisioning.LocalPathAttachResume, p.otherRoots(r.OperationID))
	if err != nil {
		return r, err
	}
	if check.CanonicalPath != r.LocalPath {
		return r, errors.New("conversion source is an alias")
	}
	j, err := p.conversionJournal(r, true)
	if err != nil {
		return r, err
	}
	backup, err := provisioning.PreflightLocalPath(j.Get().BackupPath, provisioning.LocalPathAttach, p.otherRoots(r.OperationID))
	if err != nil {
		return r, err
	}
	if backup.Exists || backup.CanonicalPath != j.Get().BackupPath {
		return r, errors.New("conversion recovery path must be absent and canonical")
	}
	r, err = p.local.QueueConversion(r, j.Get())
	if err == nil {
		p.Enqueue(r.OperationID)
	}
	return r, err
}

func conversionBound(r localrepo.Record, intent localrepo.ConversionRecord) bool {
	return !r.RemoteDeletionObserved && r.ConversionID == intent.ID && r.OperationID == intent.AttachmentID && r.ServerID == intent.ServerID &&
		r.RepoID == intent.RepoID && r.RepoURL == intent.RepoURL && r.LocalPath == intent.SourcePath && r.PendingLocalPath == intent.BackupPath &&
		r.RelocationQueued && r.RelocationMoveExisting && (r.State == localrepo.StateAttached || r.State == localrepo.StateRelocating)
}

type provisionerConversionEffects struct {
	conversionEffectsAdapter
	p       *daemonProvisioner
	profile clientprofile.Profile
}

func (e *provisionerConversionEffects) current(intent localrepo.ConversionRecord) (localrepo.Record, error) {
	r, ok := e.p.local.Get(intent.AttachmentID)
	if !ok || !conversionBound(r, intent) {
		return r, errors.New("conversion no longer owns this attachment and both paths")
	}
	profile, ok := e.p.Profile(r.ServerID)
	if !ok || profile != e.profile {
		return r, errors.New("conversion activation changed")
	}
	if e.p.relocationCheck == nil || e.p.anchorEnabled == nil || !e.p.anchorEnabled(r.ServerID) {
		return r, errors.New("conversion authority or preference is unavailable")
	}
	if err := e.p.relocationCheck(r); err != nil {
		return r, err
	}
	return r, nil
}

func (e *provisionerConversionEffects) Acquire(ctx context.Context, intent localrepo.ConversionRecord) (func(), error) {
	leave, err := e.p.waitForCreationTurn(ctx)
	if err != nil {
		return nil, err
	}
	r, err := e.current(intent)
	if err == nil && r.State == localrepo.StateAttached {
		r, err = e.p.local.StartConversion(r)
	}
	if err == nil {
		err = e.p.quiesceAttachment(ctx, r)
	} // never detach/strip metadata
	if errors.Is(err, commit.ErrLocalStatusBusy) && intent.Phase == localrepo.ConversionWaiting && !intent.Prepared {
		err = localrepo.ErrConversionWaiting
	}
	if err == nil && commit.HasPendingLocalMutation(r.LocalPath) {
		if intent.Phase == localrepo.ConversionWaiting && !intent.Prepared {
			err = localrepo.ErrConversionWaiting
		} else {
			err = commit.LocalMutationPendingError(r.LocalPath)
		}
	}
	leave() // durable per-WC fence, not a global mutex through network/copy IO
	if err != nil {
		return nil, err
	}
	return e.conversionEffectsAdapter.Acquire(ctx, intent)
}

func (e *provisionerConversionEffects) Revalidate(ctx context.Context, intent localrepo.ConversionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := e.current(intent); err != nil {
		return err
	}
	return e.conversionEffectsAdapter.Revalidate(ctx, intent)
}

func (p *daemonProvisioner) runConversion(ctx context.Context, operationID string) {
	r, ok := p.local.Get(operationID)
	if !ok || r.ConversionID == "" || r.RemoteDeletionObserved {
		return
	}
	fail := func(err error) {
		if ctx.Err() == nil {
			_, _ = p.local.RecordConversionError(operationID, r.ConversionID, err)
		}
	}
	j, err := p.conversionJournal(r, false)
	if err != nil {
		fail(err)
		return
	}
	intent := j.Get()
	if !conversionBound(r, intent) {
		fail(errors.New("conversion journal no longer matches its lifecycle reservation"))
		return
	}
	if intent.Blocked != "" {
		fail(errors.New(intent.Blocked))
		return
	}
	if p.newConversionEffects == nil {
		fail(errors.New("native conversion adapter unavailable; both paths retained"))
		return
	}
	profile, ok := p.Profile(r.ServerID)
	if !ok {
		return
	}
	adapter, err := p.newConversionEffects(r, intent)
	if err != nil {
		fail(err)
		return
	}
	if adapter == nil {
		fail(errors.New("native conversion adapter returned no implementation"))
		return
	}
	effects := &provisionerConversionEffects{adapter, p, profile}
	executor := &localrepo.ConversionExecutor{Journal: j, Effects: effects}
	outcome, err := executor.Run(ctx)
	if ctx.Err() != nil {
		return
	}
	r, ok = p.local.Get(operationID)
	if !ok || !conversionBound(r, outcome) {
		return
	}
	if errors.Is(err, localrepo.ErrConversionWaiting) && outcome.Phase == localrepo.ConversionWaiting && !outcome.Prepared {
		if _, checkErr := effects.current(outcome); checkErr != nil {
			fail(checkErr)
			return
		}
		if r.State == localrepo.StateRelocating {
			r, err = p.local.DeferConversion(r, outcome)
			if err != nil {
				fail(err)
				return
			}
		}
		p.publishLocalRecord(ctx, r, profile)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	if outcome.Phase != localrepo.ConversionComplete && outcome.Phase != localrepo.ConversionRolledBack && outcome.Phase != localrepo.ConversionCanceled {
		return
	}
	leave, err := p.waitForCreationTurn(ctx)
	if err != nil {
		return
	}
	if _, err = effects.current(outcome); err != nil {
		leave()
		fail(err)
		return
	}
	release, err := adapter.Finalize(ctx, outcome)
	if err != nil {
		if release != nil {
			release()
		}
		leave()
		fail(err)
		return
	}
	if release == nil {
		leave()
		fail(errors.New("conversion terminal root was not pinned"))
		return
	}
	r, err = effects.current(outcome)
	if err != nil {
		release()
		leave()
		fail(err)
		return
	}
	finished, err := p.local.FinishConversion(r, outcome)
	release()
	leave()
	if err != nil {
		fail(err)
		return
	}
	p.publishLocalRecord(ctx, finished, profile)
	if p.anchorChanged != nil {
		p.anchorChanged()
	}
}
