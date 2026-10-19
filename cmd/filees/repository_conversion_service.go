package main

import (
	"context"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"time"
)

type conversionObservation struct {
	Running, Known bool
	Phase          string
	Done, Total    int64
	Emitted        time.Time
}

func conversionEnded(phase localrepo.ConversionPhase) bool {
	return phase == localrepo.ConversionComplete || phase == localrepo.ConversionRolledBack || phase == localrepo.ConversionCanceled
}
func (p *daemonProvisioner) observeConversion(id string, value conversionObservation) {
	p.conversionMu.Lock()
	if p.conversionProgress == nil {
		p.conversionProgress = map[string]conversionObservation{}
	}
	old := p.conversionProgress[id]
	emit := old.Phase != value.Phase || time.Since(old.Emitted) >= time.Second || value.Known && value.Done == value.Total
	value.Emitted = old.Emitted
	if emit {
		value.Emitted = time.Now()
	}
	p.conversionProgress[id] = value
	p.conversionMu.Unlock()
	if emit && p.conversionChanged != nil {
		p.conversionChanged(id)
	}
}
func (p *daemonProvisioner) conversionPhase(id, phase string) {
	p.conversionMu.Lock()
	value := p.conversionProgress[id]
	p.conversionMu.Unlock()
	if value.Phase != phase {
		value.Phase, value.Known, value.Done, value.Total = phase, false, 0, 0
	}
	p.observeConversion(id, value)
}
func (p *daemonProvisioner) finishConversionObservation(id string) {
	p.conversionMu.Lock()
	value := p.conversionProgress[id]
	value.Running = false
	p.conversionProgress[id] = value
	p.conversionMu.Unlock()
	if p.conversionChanged != nil {
		p.conversionChanged(id)
	}
}

// Presentation reads only lifecycle/journal values; no listing, hashing or
// server request. A stale projection never grants permission to execute.
func (p *daemonProvisioner) WCConversionStatus(serverID, repoID string) *contract.WCConversionStatus {
	r, err := p.local.CurrentLifecycle(serverID, repoID)
	if err != nil || r.LocalPath == "" || r.RemoteDeletionObserved {
		return nil
	}
	status := &contract.WCConversionStatus{ServerID: serverID, RepoID: repoID, SourcePath: r.LocalPath}
	status.CanBegin = p.newConversionEffects != nil && p.anchorEnabled != nil && p.anchorEnabled(serverID) &&
		r.State == localrepo.StateAttached && !r.Anchor && !r.Sparse && r.Purpose == "" && !r.RelocationQueued && r.LastError == ""
	j, err := p.conversionJournal(r, false)
	if err != nil {
		if r.ConversionID != "" {
			status.Error, status.CanBegin = err.Error(), false
			status.OperationID, status.Phase, status.RecoveryPath = r.ConversionID, "unknown", r.PendingLocalPath
		}
		return status
	}
	intent := j.Get()
	if intent.AttachmentID != r.OperationID || intent.ServerID != serverID || intent.RepoID != repoID || intent.SourcePath != r.LocalPath {
		return nil
	}
	status.OperationID, status.Phase, status.RecoveryPath = intent.ID, string(intent.Phase), intent.BackupPath
	status.Error = intent.Blocked
	if status.Error == "" && r.ConversionID == intent.ID {
		status.Error = r.LastError
	}
	p.conversionMu.Lock()
	progress := p.conversionProgress[r.OperationID]
	p.conversionMu.Unlock()
	status.Running = progress.Running
	if progress.Running && intent.Phase == localrepo.ConversionWaiting && progress.Phase == "inspecting" {
		status.Phase = "inspecting"
	}
	if progress.Phase == status.Phase {
		status.CountersKnown, status.Done, status.Total = progress.Known, progress.Done, progress.Total
	}
	active := conversionBound(r, intent)
	if active && conversionEnded(intent.Phase) {
		status.Phase, status.CountersKnown = "finalizing", false
	}
	status.CanRetry = active && !status.Running && status.Error != ""
	status.CanRollback = active && !status.Running && intent.Phase != localrepo.ConversionCleanup && !conversionEnded(intent.Phase) &&
		(intent.Phase == localrepo.ConversionWaiting || status.Error != "")
	return status
}

func (p *daemonProvisioner) WCConversion(ctx context.Context, request contract.WCConversionPayload) (contract.WCConversionStatus, error) {
	if err := ctx.Err(); err != nil {
		return contract.WCConversionStatus{}, err
	}
	p.conversionControlMu.Lock()
	defer p.conversionControlMu.Unlock()
	refuse := func(err error) (contract.WCConversionStatus, error) { return contract.WCConversionStatus{}, err }
	r, err := p.local.CurrentLifecycle(request.ServerID, request.RepoID)
	if err != nil {
		return refuse(err)
	}
	if request.Action == "status" {
		status := p.WCConversionStatus(request.ServerID, request.RepoID)
		if status == nil {
			return refuse(errors.New("conversion attachment is unavailable"))
		}
		return *status, nil
	}
	if p.relocationCheck == nil {
		return refuse(errors.New("conversion authority is unavailable"))
	}
	if err = p.relocationCheck(r); err != nil {
		return refuse(err)
	}
	if request.Action == "begin" {
		if request.SourcePath != r.LocalPath {
			return refuse(errors.New("confirmed conversion path has changed"))
		}
		if _, err = p.queueConversion(ctx, request.ServerID, request.RepoID); err != nil {
			return refuse(err)
		}
	} else {
		j, err := p.conversionJournal(r, false)
		if err != nil {
			return refuse(err)
		}
		intent := j.Get()
		if request.OperationID != intent.ID || !conversionBound(r, intent) {
			return refuse(errors.New("conversion control targets a stale operation"))
		}
		if _, ok := p.Profile(r.ServerID); !ok {
			return refuse(errors.New("conversion activation is unavailable"))
		}
		if _, err = j.Control(intent, request.Action); err != nil {
			return refuse(err)
		}
		if _, err = p.local.RecordConversionError(r.OperationID, intent.ID, nil); err != nil {
			return refuse(err)
		}
		p.Enqueue(r.OperationID)
	}
	if p.conversionChanged != nil {
		p.conversionChanged(r.OperationID)
	}
	status := p.WCConversionStatus(request.ServerID, request.RepoID)
	if status == nil {
		return refuse(errors.New("conversion status unavailable after acceptance"))
	}
	return *status, nil
}
