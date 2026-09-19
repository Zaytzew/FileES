package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"filees/pkg/guiblob"
	"strings"
	"time"
)

type GUIBlobService interface {
	GUIBlob(context.Context, string, *guiblob.Write) (guiblob.State, error)
}

func (s *Server) SetGUIBlobService(service GUIBlobService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guiBlobs = service
}
func (s *Server) guiBlobService() GUIBlobService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.guiBlobs
}

func (s *Server) handleGUIBlob(req contract.Request, set bool) contract.Response {
	var id string
	var write *guiblob.Write
	if set {
		var payload contract.GUIBlobSetPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		id = payload.ServerID
		write = &payload.Write
		if err := write.Validate(); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
	} else {
		var payload contract.GUIBlobGetPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			return protoErr(req.RequestID, "proto.invalid_payload", nil)
		}
		id = payload.ServerID
	}
	if strings.TrimSpace(id) == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	active, ok := s.activations[id]
	s.mu.RUnlock()
	if !ok || active.ClientRole == contract.ClientRoleReadOnly || !active.CanCreateRepositories {
		return contract.ErrResponse(req.RequestID, "GUI-2001", "ERROR", "NONE", "gui.state_forbidden", nil)
	}
	service := s.guiBlobService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "GUI-0001", "ERROR", "RETRY", "gui.state_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := service.GUIBlob(ctx, id, write)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "GUI-1001", "ERROR", "RETRY", "gui.state_unavailable", nil)
	}
	if err = state.Validate(); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	s.mu.RLock()
	current, stillActive := s.activations[id]
	s.mu.RUnlock()
	if !stillActive || current.RealmID != active.RealmID || current.ClientID != active.ClientID || current.ClientRole == contract.ClientRoleReadOnly || !current.CanCreateRepositories {
		return contract.ErrResponse(req.RequestID, "GUI-2001", "ERROR", "NONE", "gui.state_forbidden", nil)
	}
	// A replaced activation cannot return data into a new realm's GUI cache.
	if active.RealmID != "" && state.RealmID != active.RealmID {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	return contract.OKResponse(req.RequestID, state)
}
