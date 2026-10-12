package ipcserver

import (
	"context"
	"time"

	contract "filees/pkg/contract/v1"
)

type ExplorerShortcutService interface {
	ExplorerShortcut(context.Context, string, string, *bool, string) (contract.RepoExplorerShortcutStatus, error)
}

func (s *Server) SetExplorerShortcutService(service ExplorerShortcutService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.explorerShortcut = service
}
func (s *Server) explorerShortcutService() ExplorerShortcutService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.explorerShortcut
}
func (s *Server) handleExplorerShortcut(req contract.Request) contract.Response {
	var p contract.RepoExplorerShortcutPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" || (p.Visible != nil && p.WCID == "") || (req.RepoID != "" && req.RepoID != p.RepoID) {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	service := s.explorerShortcutService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2009", "ERROR", "NONE", "head.anchor_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := service.ExplorerShortcut(ctx, p.ServerID, p.RepoID, p.Visible, p.WCID)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2011", "ERROR", "REQUIRE_ACTION", "head.anchor_refused", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, result)
}
