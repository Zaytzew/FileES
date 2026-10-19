package main

import (
	"context"
	"errors"
	"filees/pkg/clientview"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
)

type conversionQuiesceIntent struct{ AttachmentID, ConversionID, SourcePath, URL string }

// A missing runtime after restart is expected only for this exact ongoing
// conversion, never proof that an arbitrary attached folder is stopped.
func conversionQuiesceResumeAllowed(request provisionedAttachment, view clientview.View, lifecycle *localrepo.Store) bool {
	proof := request.Conversion
	if !request.Quiesce || request.Detach || proof == nil || proof.ConversionID == "" || lifecycle == nil {
		return false
	}
	r, err := lifecycle.CurrentLifecycle(request.Repo.ServerID, request.Repo.ID)
	if err != nil || r.State != localrepo.StateRelocating || r.OperationID != proof.AttachmentID || r.ConversionID != proof.ConversionID || r.LocalPath != proof.SourcePath || r.RepoURL != proof.URL || !r.RelocationQueued || !r.RelocationMoveExisting || r.PendingLocalPath == "" || r.RemoteDeletionObserved || r.LastError != "" || r.Anchor || r.Sparse || r.Purpose != "" {
		return false
	}
	matches := 0
	for _, repo := range view.Repositories {
		if repo.RepoID != r.RepoID {
			continue
		}
		matches++
		if repo.State != "active" || repo.URL != r.RepoURL || (repo.Access != "r" && repo.Access != "rw") || repo.Access != r.Access || repo.Purpose != "" {
			return false
		}
	}
	return matches == 1
}

func quiesceProjectedRuntime(ctx context.Context, supervisor *reposupervisor.Supervisor, request provisionedAttachment, view clientview.View, runtimes map[reposupervisor.Key]repoRuntime, lifecycle *localrepo.Store) error {
	key := reposupervisor.Key{ServerID: request.Repo.ServerID, RepoID: request.Repo.ID}
	old, exists := runtimes[key]
	if !exists && !conversionQuiesceResumeAllowed(request, view, lifecycle) {
		return errors.New("attached runtime or authoritative projection is unavailable")
	}
	delete(runtimes, key)
	// Reapply even when the registry is empty: the supervisor must stop any
	// surviving child and enforce its current-generation barrier. No bypass.
	desired := attachedProjection(key.ServerID, view, runtimes)
	err := supervisor.ApplyLocalAttachment(ctx, key.ServerID, view.Generation, desired, nil)
	if err != nil && exists {
		runtimes[key] = old
	}
	return err
}
