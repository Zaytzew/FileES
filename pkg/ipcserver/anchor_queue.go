package ipcserver

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"filees/pkg/anchorqueue"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

type AnchorReleaseQueueService interface {
	Queue(context.Context, contract.RepoAnchorQueuePayload) (contract.RepoAnchorQueueResult, error)
	QueuedReleases() []contract.RepoAnchorQueueResult
}

func (s *Server) anchorQueueService() AnchorReleaseQueueService {
	service, _ := s.anchorBatchService().(AnchorReleaseQueueService)
	return service
}

func (s *Server) handleAnchorQueue(req contract.Request, submit bool) contract.Response {
	var status contract.RepoAnchorQueueStatusPayload
	var p contract.RepoAnchorQueuePayload
	if submit {
		if err := contract.DecodePayload(req.Payload, &p); err != nil || p.Action != "release" || !validAnchorBatchSelection(p.RepoAnchorPlanPayload) || p.Files <= 0 || p.Files > 20000 {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		raw, err := hex.DecodeString(p.Fingerprint)
		if err != nil || len(raw) != 32 {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		status = contract.RepoAnchorQueueStatusPayload{ServerID: p.ServerID, RepoID: p.RepoID, IntentID: p.IntentID}
	} else if err := contract.DecodePayload(req.Payload, &status); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if _, err := uuid.Parse(status.IntentID); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if _, _, refusal, ok := s.headRepo(req.RequestID, status.ServerID, status.RepoID); !ok {
		return refusal
	}
	service := s.anchorQueueService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2014", "ERROR", "NONE", "head.release_unavailable", nil)
	}
	if !submit {
		for _, result := range service.QueuedReleases() {
			if result.IntentID == status.IntentID && result.ServerID == status.ServerID && result.RepoID == status.RepoID {
				return contract.OKResponse(req.RequestID, result)
			}
		}
		return contract.ErrResponse(req.RequestID, "HEAD-2020", "ERROR", "NONE", "head.folder_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := service.Queue(ctx, p)
	if err == nil {
		return contract.OKResponse(req.RequestID, result)
	}
	if errors.Is(err, anchorqueue.ErrFull) {
		return contract.ErrResponse(req.RequestID, "HEAD-2024", "ERROR", "REQUIRE_ACTION", "head.release_queue_full", nil)
	}
	code, key, hint := AnchorBatchFailure(err, "release")
	return contract.ErrResponse(req.RequestID, code, "ERROR", hint, key, nil)
}

func (s *Server) visibleQueuedReleases() []contract.RepoAnchorQueueResult {
	service := s.anchorQueueService()
	if service == nil {
		return nil
	}
	var results []contract.RepoAnchorQueueResult
	for _, result := range service.QueuedReleases() {
		if _, _, _, ok := s.headRepo("release-queue-projection", result.ServerID, result.RepoID); ok {
			results = append(results, result)
		}
	}
	return results
}
