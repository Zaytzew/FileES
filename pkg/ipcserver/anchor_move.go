package ipcserver

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
)

type AnchorMoveService interface {
	MovePlan(context.Context, contract.RepoAnchorMoveSelection) (contract.RepoAnchorMovePlan, error)
	MoveExecute(context.Context, contract.RepoAnchorMovePayload) error
	MoveRecover(context.Context, string, string) error
}

func (s *Server) SetAnchorMove(service AnchorMoveService) {
	s.mu.Lock()
	s.anchorMove = service
	s.mu.Unlock()
}
func (s *Server) anchorMoveService() AnchorMoveService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.anchorMove
}

func validAnchorMoveSelection(p contract.RepoAnchorMoveSelection) bool {
	if p.ServerID == "" || p.RepoID == "" {
		return false
	}
	for _, rel := range []string{p.Source, p.Destination} {
		clean, ok := cleanHeadPath(rel)
		if !ok || clean != rel || rel == "" || !fileReleaseSelection(rel) || len(rel) > 4096 {
			return false
		}
	}
	a, b := strings.ToLower(p.Source), strings.ToLower(p.Destination)
	return a != b && !strings.HasPrefix(a, b+"/") && !strings.HasPrefix(b, a+"/")
}
func (s *Server) handleAnchorMove(req contract.Request) contract.Response {
	var p contract.RepoAnchorMovePayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	recovery := req.Command == contract.CmdRepoAnchorMoveRecover
	if !recovery && !validAnchorMoveSelection(p.RepoAnchorMoveSelection) {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	_, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	if summary.Access != "rw" {
		return contract.ErrResponse(req.RequestID, "HEAD-2024", "ERROR", "NONE", "head.move_read_only", nil)
	}
	service := s.anchorMoveService()
	if service == nil || !summary.Attached || !summary.Sparse || !filepath.IsAbs(summary.LocalPath) {
		return contract.ErrResponse(req.RequestID, "HEAD-2020", "ERROR", "NONE", "head.folder_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var err error
	switch req.Command {
	case contract.CmdRepoAnchorMovePlan:
		var plan contract.RepoAnchorMovePlan
		plan, err = service.MovePlan(ctx, p.RepoAnchorMoveSelection)
		if err == nil {
			return contract.OKResponse(req.RequestID, plan)
		}
	case contract.CmdRepoAnchorMove:
		digest, e := hex.DecodeString(p.Fingerprint)
		if e != nil || len(digest) != 32 {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		err = service.MoveExecute(ctx, p)
	case contract.CmdRepoAnchorMoveRecover:
		// Recovery consumes persisted scope only, never a supplied replacement.
		if p.Source != "" || p.Destination != "" || p.Fingerprint != "" {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		err = service.MoveRecover(ctx, p.ServerID, p.RepoID)
	}
	if err == nil {
		return contract.OKResponse(req.RequestID, contract.RepoAnchorMoveResult{State: "moved"})
	}
	s.lg.Warnf("Explorer tree move refused: %v", err)
	if commit.HasPendingRemoteTree(summary.LocalPath) || errors.Is(err, commit.ErrRemoteTreePending) {
		return contract.ErrResponse(req.RequestID, "HEAD-2019", "ERROR", "REQUIRE_ACTION", "head.folder_recovery_required", nil)
	}
	if errors.Is(err, ErrAnchorSelectionChanged) || errors.Is(err, commit.ErrLocalStatusBusy) {
		code, key, hint := AnchorBatchFailure(err, "")
		return contract.ErrResponse(req.RequestID, code, "ERROR", hint, key, nil)
	}
	return contract.ErrResponse(req.RequestID, "HEAD-2025", "ERROR", "REQUIRE_ACTION", "head.move_refused", nil)
}
