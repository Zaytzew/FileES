package ipcserver

import (
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/runtime"
	"github.com/google/uuid"
)

func (s *Server) pauseStatus() contract.SyncPauseStatus {
	manual, draft, draining := s.syncPause.Status()
	return contract.SyncPauseStatus{Manual: manual, Draft: draft, Draining: draining}
}
func (s *Server) handleSyncPause(req contract.Request) contract.Response {
	var p contract.SyncPausePayload
	if contract.DecodePayload(req.Payload, &p) != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.syncPause.SetManual(p.Paused)
	return contract.OKResponse(req.RequestID, s.pauseStatus())
}
func draftError(id string, err error) contract.Response {
	key := "shout.draft_expired"
	code := "SHOUT-1006"
	if errors.Is(err, runtime.ErrSyncPaused) {
		key = "sync.paused"
		code = "SYNC-1001"
	}
	return contract.ErrResponse(id, code, "ERROR", "REQUIRE_ACTION", key, nil)
}
func (s *Server) handleShoutDraft(req contract.Request) contract.Response {
	var p contract.ShoutDraftPayload
	if contract.DecodePayload(req.Payload, &p) != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if _, err := uuid.Parse(p.Token); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if p.Action != "begin" && p.Action != "renew" && p.Action != "end" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if p.Action != "end" && s.repoByID(req.RepoID) == nil {
		return protoErr(req.RequestID, "proto.repo_not_found", nil)
	}
	ready, err := s.syncPause.Draft(p.Token, req.RepoID, p.Action)
	if err != nil {
		return draftError(req.RequestID, err)
	}
	return contract.OKResponse(req.RequestID, contract.ShoutDraftResult{Ready: ready})
}
