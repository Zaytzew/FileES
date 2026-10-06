package ipcserver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
)

// Only the daemon's current provider may implement this. Unlike downloads,
// release has no depth-based fallback for ordinary working copies.
type AnchorRelease func(context.Context, string, string, string) error

func (s *Server) SetAnchorRelease(release AnchorRelease) {
	s.mu.Lock()
	s.anchorRelease = release
	s.mu.Unlock()
}

func (s *Server) anchorReleaseHandler() AnchorRelease {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.anchorRelease
}

func (s *Server) handleAnchorRelease(req contract.Request) contract.Response {
	var p contract.RepoAnchorReleasePayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	_, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	release := s.anchorReleaseHandler()
	if release == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2014", "ERROR", "NONE", "head.release_unavailable", nil)
	}
	rel, valid := cleanHeadPath(p.Path)
	if !valid || rel == "" || rel != p.Path || !fileReleaseSelection(rel) {
		return contract.ErrResponse(req.RequestID, "HEAD-2002", "ERROR", "NONE", "head.bad_path", nil)
	}
	if !summary.Attached || !filepath.IsAbs(summary.LocalPath) {
		return contract.ErrResponse(req.RequestID, "HEAD-2005", "ERROR", "REQUIRE_ACTION", "head.anchor_required", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := release(ctx, p.ServerID, p.RepoID, rel); err != nil {
		s.lg.Warnf("local file release refused: %v", err)
		if errors.Is(err, commit.ErrFileReleasePending) || commit.HasPendingFileRelease(summary.LocalPath) {
			return contract.ErrResponse(req.RequestID, "HEAD-2016", "ERROR", "RETRY", "head.release_pending", nil)
		}
		return contract.ErrResponse(req.RequestID, "HEAD-2015", "ERROR", "RETRY", "head.release_refused", nil)
	}
	return contract.OKResponse(req.RequestID, contract.RepoHeadWriteResult{LocalPath: summary.LocalPath, Path: rel, State: "attached"})
}

func fileReleaseSelection(rel string) bool {
	if strings.ContainsAny(rel, "\\:\x00\r\n\t") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") {
			return false
		}
	}
	return true
}
