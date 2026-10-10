package main

import (
	"context"
	"errors"
	"slices"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"github.com/google/uuid"
)

type headBrowserReleaseQueueDaemon interface {
	AnchorReleaseQueue(context.Context, contract.RepoAnchorQueuePayload) (*contract.RepoAnchorQueueResult, error)
	AnchorReleaseQueueStatus(context.Context, contract.RepoAnchorQueueStatusPayload) (*contract.RepoAnchorQueueResult, error)
}

// Confirmation is submitted once. All following calls only read that ID,
// including after a lost submit response; no status error replays the action.
func (s *HeadBrowserService) queuedFolderRelease(ctx context.Context, d headBrowserReleaseQueueDaemon, repo HeadBrowserRepository, focus HeadBrowserContext,
	p contract.RepoAnchorPlanPayload, approved contract.RepoAnchorPlan) (contract.RepoAnchorBatchResult, contract.RepoAnchorPlan, error) {
	if err := s.folderCurrent(repo, focus, "release"); err != nil {
		return contract.RepoAnchorBatchResult{}, approved, s.folderFailure(err)
	}
	request := contract.RepoAnchorQueuePayload{RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: approved.Fingerprint}, IntentID: uuid.NewString(), Files: approved.Files}
	status := contract.RepoAnchorQueueStatusPayload{ServerID: p.ServerID, RepoID: p.RepoID, IntentID: request.IntentID}
	result, err := d.AnchorReleaseQueue(ctx, request)
	if err != nil {
		// Typed refusal proves no accepted intention; transport failure does
		// not. Never submit a second ID to resolve a lost response.
		var refusal *ipcclient.ResponseError
		if errors.As(err, &refusal) && (refusal.Body.Code == "HEAD-2014" || refusal.Body.Code == "HEAD-2017" || refusal.Body.Code == "HEAD-2024" || refusal.Body.Code == "HEAD-2001" || refusal.Body.MessageKey == "proto.invalid_payload") {
			return contract.RepoAnchorBatchResult{}, approved, s.folderFailure(err)
		}
		result, err = d.AnchorReleaseQueueStatus(ctx, status)
	}
	for {
		if err != nil || result == nil {
			return contract.RepoAnchorBatchResult{State: "queue_unknown", Path: p.Path, LocalPath: repo.LocalPath}, approved, nil
		}
		if result.IntentID != status.IntentID || result.ServerID != p.ServerID || result.RepoID != p.RepoID || result.Path != p.Path || result.LocalPath != repo.LocalPath || result.Total != approved.Files || result.Completed < 0 || result.Completed > result.Total {
			return contract.RepoAnchorBatchResult{}, approved, s.folderFailure(errors.New("invalid release queue response"))
		}
		switch result.State {
		case "attached", "failed":
			return result.RepoAnchorBatchResult, approved, nil
		case "queued", "running":
		default:
			return contract.RepoAnchorBatchResult{}, approved, s.folderFailure(errors.New("unknown release queue state"))
		}
		if err := s.waitFolder(ctx, time.Second); err != nil {
			return contract.RepoAnchorBatchResult{State: "queued", Path: p.Path, LocalPath: repo.LocalPath}, approved, nil
		}
		result, err = d.AnchorReleaseQueueStatus(ctx, status)
	}
}

func (s *HeadBrowserService) releaseQueueOffered() bool {
	snapshot := s.projection()
	return snapshot.Connected && !snapshot.Stale && slices.Contains(snapshot.Capabilities, contract.CapRepoAnchorReleaseQueue)
}
