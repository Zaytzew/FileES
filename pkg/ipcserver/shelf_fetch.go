package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"time"
)

type ShelfFetchService interface {
	BeginShelfFetch(serverID, repoID, repoURL, localPath string, item contract.ShelfItem) (contract.RepoLifecycleResult, error)
	InspectShelf(serverID, repoID string) contract.RepoLifecycleResult
}

func (s *Server) handleShelfFetch(req contract.Request) contract.Response {
	local, ok := s.repositoryLifecycleService().(ShelfFetchService)
	remote := s.uploadChannelService()
	if !ok || remote == nil {
		return contract.ErrResponse(req.RequestID, "UPLOAD-0001", "ERROR", "RETRY", "upload_channel.unavailable", nil)
	}
	var p contract.ShelfFetchPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || (!p.InspectOnly && p.UploadID == "") || p.ChannelID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	a, exists := s.activations[p.ServerID]
	s.mu.RUnlock()
	forbidden := func() contract.Response {
		return contract.ErrResponse(req.RequestID, "UPLOAD-2001", "ERROR", "NONE", "upload_channel.forbidden", nil)
	}
	if !exists || a.ClientRole == contract.ClientRoleReadOnly || !a.CanCreateRepositories || a.RealmID == "" {
		return forbidden()
	}
	parent := s.RepoState(p.ServerID, p.RepoID)
	if parent == nil {
		return forbidden()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	channels, err := remote.ListUploadChannels(ctx, p.ServerID, p.RepoID)
	if err != nil {
		return forbidden()
	}
	shelfID := ""
	for _, ch := range channels.Channels {
		if ch.ChannelID == p.ChannelID && ch.AuthorityRepoID == p.RepoID {
			shelfID = ch.UploadRepoID
			break
		}
	}
	if shelfID == "" {
		return forbidden()
	}
	repo := s.RepoState(p.ServerID, shelfID)
	if repo == nil {
		return forbidden()
	}
	view := repo.Snapshot()
	if view.Purpose != contract.RepoPurposeUploadShelf || view.Access != "r" {
		return forbidden()
	}
	if repo.ProjectedState() != contract.StateActive {
		return forbidden()
	}
	if p.InspectOnly {
		return contract.OKResponse(req.RequestID, local.InspectShelf(p.ServerID, shelfID))
	}
	listing, err := remote.ListShelf(ctx, p.ServerID, p.ChannelID)
	if err != nil || listing.ChannelID != p.ChannelID {
		return forbidden()
	}
	for _, item := range listing.Items {
		if item.UploadID != p.UploadID {
			continue
		}
		result, err := local.BeginShelfFetch(p.ServerID, shelfID, repo.Summary().URL, p.LocalPath, item)
		if err != nil {
			s.lg.Warnf("shelf download rejected: %v", err)
			return contract.ErrResponse(req.RequestID, "REPO-2002", "ERROR", "REQUIRE_ACTION", "repo.invalid_local_intent", nil)
		}
		return contract.OKResponse(req.RequestID, result)
	}
	return forbidden()
}
