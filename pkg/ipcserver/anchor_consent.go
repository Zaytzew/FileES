package ipcserver

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"time"

	contract "filees/pkg/contract/v1"
)

type AnchorConsent func(context.Context, contract.RepoAnchorConsentPayload) error

func (s *Server) SetAnchorConsent(fn AnchorConsent) {
	s.mu.Lock()
	s.anchorConsent = fn
	s.mu.Unlock()
}

func (s *Server) anchorConsentHandler() AnchorConsent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.anchorConsent
}

// Snapshot reads the live guard after releasing the repo mutex. Returning a
// copy prevents a caller from mutating a guard through an IPC projection.
func (rs *RepoState) SetAnchorBlocksSource(fn func() []contract.AnchorDownloadBlock) {
	rs.mu.Lock()
	rs.anchorBlocksFn = fn
	rs.mu.Unlock()
}

// SetAnchorDamageSource reports unreadable entries of the repository's virtual
// folder. The source returns a fresh value, or nil when there are none.
func (rs *RepoState) SetAnchorDamageSource(fn func() *contract.AnchorDamage) {
	rs.mu.Lock()
	rs.anchorDamageFn = fn
	rs.mu.Unlock()
}

// Read after releasing the repository mutex, just like the damage source.
func (rs *RepoState) SetAnchorRuntimeSource(fn func() *contract.AnchorRuntimeStatus) {
	rs.mu.Lock()
	rs.anchorRuntimeFn = fn
	rs.mu.Unlock()
}

func (s *Server) handleAnchorConsent(req contract.Request) contract.Response {
	var p contract.RepoAnchorConsentPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	id, err := hex.DecodeString(p.BlockID)
	if err != nil || len(id) != 16 {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	_, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	fn := s.anchorConsentHandler()
	if fn == nil || !summary.Attached || !filepath.IsAbs(summary.LocalPath) {
		return contract.ErrResponse(req.RequestID, "HEAD-2005", "ERROR", "NONE", "head.anchor_required", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fn(ctx, p); err != nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2006", "ERROR", "NONE", "head.materialize_failed", nil)
	}
	return contract.OKResponse(req.RequestID, contract.RepoAnchorConsentResult{Allowed: p.Allow})
}
