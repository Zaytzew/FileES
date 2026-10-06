package ipcserver

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"time"

	contract "filees/pkg/contract/v1"
)

type AnchorBatchService interface {
	Plan(context.Context, contract.RepoAnchorPlanPayload) (contract.RepoAnchorPlan, error)
	Execute(context.Context, contract.RepoAnchorBatchPayload) (contract.RepoAnchorBatchResult, error)
}

func (s *Server) SetAnchorBatch(service AnchorBatchService) {
	s.mu.Lock()
	s.anchorBatch = service
	s.mu.Unlock()
}

func (s *Server) anchorBatchService() AnchorBatchService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.anchorBatch
}

func validAnchorBatchSelection(p contract.RepoAnchorPlanPayload) bool {
	if p.ServerID == "" || p.RepoID == "" || (p.Action != "download" && p.Action != "release") {
		return false
	}
	if p.Path == "." {
		return true
	}
	rel, ok := cleanHeadPath(p.Path)
	return ok && rel != "" && rel == p.Path && fileReleaseSelection(rel)
}

func (s *Server) handleAnchorBatch(req contract.Request, execute bool) contract.Response {
	var p contract.RepoAnchorBatchPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || !validAnchorBatchSelection(p.RepoAnchorPlanPayload) {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	_, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	service := s.anchorBatchService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2014", "ERROR", "NONE", "head.release_unavailable", nil)
	}
	if !summary.Attached || !filepath.IsAbs(summary.LocalPath) {
		return contract.ErrResponse(req.RequestID, "HEAD-2005", "ERROR", "REQUIRE_ACTION", "head.anchor_required", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if !execute {
		plan, err := service.Plan(ctx, p.RepoAnchorPlanPayload)
		if err == nil {
			return contract.OKResponse(req.RequestID, plan)
		}
		s.lg.Warnf("Explorer folder preview refused: %v", err)
	} else {
		fingerprint, err := hex.DecodeString(p.Fingerprint)
		if err != nil || len(fingerprint) != 32 {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		result, err := service.Execute(ctx, p)
		if err == nil {
			return contract.OKResponse(req.RequestID, result)
		}
		s.lg.Warnf("Explorer folder action refused: %v", err)
	}
	code, key := "HEAD-2015", "head.release_refused"
	if p.Action == "download" {
		code, key = "HEAD-2006", "head.materialize_failed"
	}
	return contract.ErrResponse(req.RequestID, code, "ERROR", "RETRY", key, nil)
}
