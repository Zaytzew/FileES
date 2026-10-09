package ipcserver

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
)

var ErrAnchorSelectionChanged = errors.New("folder selection changed; preview and confirm again")
var ErrAnchorRecoveryRequired = errors.New("folder publication requires verified recovery")
var ErrAnchorPathMissing = errors.New("repository path is absent from the local folder")

// Typed reasons only. Never infer permission to retry from command text or
// diagnostic Details. Partial results may expose this reason, not raw output.
func AnchorBatchFailure(err error, action string) (code, key, hint string) {
	switch {
	case errors.Is(err, commit.ErrFileReleasePending):
		return "HEAD-2016", "head.release_pending", "REQUIRE_ACTION"
	case errors.Is(err, ErrAnchorRecoveryRequired):
		return "HEAD-2019", "head.folder_recovery_required", "REQUIRE_ACTION"
	case errors.Is(err, ErrAnchorPathMissing):
		return "HEAD-2023", "head.folder_path_missing", "REQUIRE_ACTION"
	case errors.Is(err, commit.ErrLocalStatusBusy):
		return "HEAD-2018", "head.folder_busy", "RETRY_LOCAL"
	case errors.Is(err, ErrAnchorSelectionChanged):
		return "HEAD-2017", "head.folder_changed", "REQUIRE_ACTION"
	case errors.Is(err, ErrDepthUnavailable):
		return "HEAD-2020", "head.folder_unavailable", "NONE"
	case errors.Is(err, context.DeadlineExceeded):
		return "HEAD-2021", "head.folder_wait_timeout", "RETRY_LOCAL"
	case errors.Is(err, os.ErrPermission):
		return "HEAD-2022", "head.folder_permission", "REQUIRE_ACTION"
	}
	if action == "release" {
		return "HEAD-2015", "head.release_refused", "RETRY"
	}
	return "HEAD-2006", "head.materialize_failed", "RETRY"
}

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
	var actionErr error
	if !execute {
		plan, err := service.Plan(ctx, p.RepoAnchorPlanPayload)
		if err == nil {
			return contract.OKResponse(req.RequestID, plan)
		}
		if !errors.Is(err, commit.ErrLocalStatusBusy) {
			s.lg.Warnf("Explorer folder preview refused: %v", err)
		}
		actionErr = err
	} else {
		fingerprint, err := hex.DecodeString(p.Fingerprint)
		if err != nil || len(fingerprint) != 32 {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		result, err := service.Execute(ctx, p)
		if err == nil {
			return contract.OKResponse(req.RequestID, result)
		}
		if !errors.Is(err, commit.ErrLocalStatusBusy) && !errors.Is(err, ErrAnchorSelectionChanged) {
			s.lg.Warnf("Explorer folder action refused: %v", err)
		}
		actionErr = err
	}
	if errors.Is(actionErr, commit.ErrFileReleasePending) || commit.HasPendingFileRelease(summary.LocalPath) {
		return contract.ErrResponse(req.RequestID, "HEAD-2016", "ERROR", "RETRY", "head.release_pending", nil)
	}
	code, key, hint := AnchorBatchFailure(actionErr, p.Action)
	return contract.ErrResponse(req.RequestID, code, "ERROR", hint, key, nil)
}
