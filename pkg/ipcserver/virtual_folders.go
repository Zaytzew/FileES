package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"time"
)

type VirtualFoldersService interface {
	VirtualFolders(context.Context, string, *bool) (contract.VirtualFoldersStatus, error)
}

func (s *Server) SetVirtualFoldersService(service VirtualFoldersService) {
	s.mu.Lock()
	s.virtualFolders = service
	s.mu.Unlock()
}
func (s *Server) virtualFoldersService() VirtualFoldersService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.virtualFolders
}
func (s *Server) handleVirtualFolders(req contract.Request) contract.Response {
	var p contract.VirtualFoldersPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	service := s.virtualFoldersService()
	if p.ServerID == "" && p.Enabled != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	if service == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2009", "ERROR", "NONE", "head.anchor_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := service.VirtualFolders(ctx, p.ServerID, p.Enabled)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2011", "ERROR", "REQUIRE_ACTION", "head.anchor_refused", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, result)
}
