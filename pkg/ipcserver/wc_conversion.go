package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
	"strings"
	"time"
)

// Status is daemon-local presentation only. Mutation must independently check
// lifecycle identity, activation, current authority and local preference.
type WCConversionService interface {
	WCConversion(context.Context, contract.WCConversionPayload) (contract.WCConversionStatus, error)
	WCConversionStatus(string, string) *contract.WCConversionStatus
}

func (s *Server) SetWCConversionService(service WCConversionService) {
	s.mu.Lock()
	s.wcConversion = service
	s.mu.Unlock()
}
func (s *Server) conversionService() WCConversionService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wcConversion
}
func (s *Server) conversionStatus(serverID, repoID string) *contract.WCConversionStatus {
	if service := s.conversionService(); service != nil {
		return service.WCConversionStatus(serverID, repoID)
	}
	return nil
}
func (s *Server) handleWCConversion(req contract.Request) contract.Response {
	var p contract.WCConversionPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || strings.TrimSpace(p.ServerID) == "" || strings.TrimSpace(p.RepoID) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	switch p.Action {
	case "begin":
		if p.SourcePath == "" || p.OperationID != "" {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
	case "status":
		if p.SourcePath != "" || p.OperationID != "" {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
	case "retry", "rollback":
		if uuid.Validate(p.OperationID) != nil || p.SourcePath != "" {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
	default:
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	_, activated := s.activations[p.ServerID]
	s.mu.RUnlock()
	service := s.conversionService()
	if !activated || service == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2009", "ERROR", "NONE", "head.anchor_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := service.WCConversion(ctx, p)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2002", "ERROR", "REQUIRE_ACTION", "repo.invalid_local_intent", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, result)
}
