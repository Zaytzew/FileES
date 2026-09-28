package ipcserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
)

// HeadService reads HEAD without a working copy: one directory listing and one
// file at a time. implementation notes (not distributed)
//
// Writing is deliberately not here. The first chosen path goes through the
// ordinary attach lifecycle in sparse mode, so the copy is supervised from its
// first moment; later paths and "fill" go through the running working copy,
// under its commit service's operation lock. A raw checkout from this service
// would produce a working copy the daemon neither watches nor commits.
type HeadService interface {
	HeadList(ctx context.Context, serverID, repoURL, path string) ([]contract.RepoHeadEntry, error)
	HeadCat(ctx context.Context, serverID, repoURL, path string) (string, error)
}

// SparseAttacher is the lifecycle's sparse entry point. It is optional on
// RepositoryLifecycleService so existing implementations need not change.
type SparseAttacher interface {
	BeginSparseAttach(serverID, repoID, localPath, sparsePath string, required bool) (contract.RepoLifecycleResult, error)
}

// FullDepthRecorder records that a sparse copy now holds the whole tree.
type FullDepthRecorder interface {
	MarkFullDepth(serverID, repoID string) error
}

func (s *Server) SetHeadService(service HeadService) {
	s.mu.Lock()
	s.headReads = service
	s.mu.Unlock()
}

func (s *Server) headService() HeadService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.headReads
}

// headRepo resolves a repository a member may read at HEAD. Access comes from
// the projection; the server's authz is still the final word on every read.
// An activation awaiting approval has no repository access in its projection
// and is refused here, before anything reaches the server.
func (s *Server) headRepo(requestID, serverID, repoID string) (*RepoState, contract.RepoSummary, contract.Response, bool) {
	repo := s.RepoState(serverID, repoID)
	if repo == nil {
		return nil, contract.RepoSummary{}, contract.ErrResponse(requestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil), false
	}
	summary := repo.Summary()
	s.mu.RLock()
	_, activated := s.activations[serverID]
	s.mu.RUnlock()
	if !activated || summary.URL == "" || (summary.Access != "r" && summary.Access != "rw") || summary.Purpose == contract.RepoPurposeUploadShelf {
		return nil, contract.RepoSummary{}, contract.ErrResponse(requestID, "HEAD-2001", "ERROR", "NONE", "head.forbidden", nil), false
	}
	return repo, summary, contract.Response{}, true
}

func cleanHeadPath(raw string) (string, bool) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return "", true
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, "\x00\r\n") {
			return "", false
		}
	}
	return raw, true
}

// markLocal flags the entries already present in the sparse working copy, so
// the browser can say "on this computer" next to them.
func markLocal(localPath, dir string, entries []contract.RepoHeadEntry) {
	if !filepath.IsAbs(localPath) {
		return
	}
	base := filepath.Join(localPath, filepath.FromSlash(dir))
	for i := range entries {
		if _, err := os.Lstat(filepath.Join(base, entries[i].Name)); err == nil {
			entries[i].Local = true
		}
	}
}

func (s *Server) handleHeadList(req contract.Request) contract.Response {
	var p contract.RepoHeadListPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	service := s.headService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-0001", "ERROR", "RETRY", "head.unavailable", nil)
	}
	_, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	path, good := cleanHeadPath(p.Path)
	if !good {
		return contract.ErrResponse(req.RequestID, "HEAD-2002", "ERROR", "NONE", "head.bad_path", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	entries, err := service.HeadList(ctx, p.ServerID, summary.URL, path)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2003", "ERROR", "RETRY", "head.list_failed", map[string]string{"detail": err.Error()})
	}
	if entries == nil {
		entries = []contract.RepoHeadEntry{}
	}
	if summary.Attached {
		markLocal(summary.LocalPath, path, entries)
	}
	return contract.OKResponse(req.RequestID, contract.RepoHeadListResult{Path: path, Entries: entries})
}

func (s *Server) handleHeadCat(req contract.Request) contract.Response {
	var p contract.RepoHeadCatPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	service := s.headService()
	if service == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-0001", "ERROR", "RETRY", "head.unavailable", nil)
	}
	_, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	path, good := cleanHeadPath(p.Path)
	if !good || path == "" {
		return contract.ErrResponse(req.RequestID, "HEAD-2002", "ERROR", "NONE", "head.bad_path", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	file, err := service.HeadCat(ctx, p.ServerID, summary.URL, path)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2004", "ERROR", "RETRY", "head.cat_failed", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, contract.RepoHeadCatResult{Path: path, File: file})
}

// handleHeadMaterialize brings one path onto this computer. The first path of
// a repository without a working copy starts a sparse attachment at LocalPath
// (the anchor the user picked); every later path deepens that same copy.
func (s *Server) handleHeadMaterialize(req contract.Request) contract.Response {
	var p contract.RepoHeadMaterializePayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	repo, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	path, good := cleanHeadPath(p.Path)
	if !good || path == "" {
		return contract.ErrResponse(req.RequestID, "HEAD-2002", "ERROR", "NONE", "head.bad_path", nil)
	}
	if summary.Attached && filepath.IsAbs(summary.LocalPath) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := repo.SetDepth(ctx, path, "infinity"); err != nil {
			if errors.Is(err, ErrDepthUnavailable) {
				return contract.ErrResponse(req.RequestID, "HEAD-2008", "ERROR", "RETRY", "head.copy_not_running", nil)
			}
			return contract.ErrResponse(req.RequestID, "HEAD-2006", "ERROR", "RETRY", "head.materialize_failed", map[string]string{"detail": err.Error()})
		}
		return contract.OKResponse(req.RequestID, contract.RepoHeadWriteResult{LocalPath: summary.LocalPath, Path: path, State: "attached"})
	}
	// One kind of partial attachment per build: where Explorer anchors exist,
	// a repository without a copy is attached as an anchor, never as a plain
	// sparse copy (owner, 2026-09-28).
	if s.partialAnchorMode() {
		return contract.ErrResponse(req.RequestID, "HEAD-2012", "ERROR", "REQUIRE_ACTION", "head.partial_is_anchor", nil)
	}
	local := strings.TrimSpace(p.LocalPath)
	if !filepath.IsAbs(local) {
		return contract.ErrResponse(req.RequestID, "HEAD-2005", "ERROR", "REQUIRE_ACTION", "head.anchor_required", nil)
	}
	service := s.repositoryLifecycleService()
	sparse, canSparse := service.(SparseAttacher)
	if service == nil || !canSparse {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	if repo.ProjectedState() != contract.StateActive {
		return contract.ErrResponse(req.RequestID, "REPO-2004", "ERROR", "RETRY", "repo.not_attachable", nil)
	}
	begun, err := sparse.BeginSparseAttach(p.ServerID, p.RepoID, local, path, summary.AttachmentPolicy == "required")
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2002", "ERROR", "REQUIRE_ACTION", "repo.invalid_local_intent", map[string]string{"detail": err.Error()})
	}
	approved, err := service.ApproveAttach(begun.OperationID, p.ServerID, p.RepoID, summary.URL, summary.Access)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2005", "ERROR", "REQUIRE_ACTION", "repo.attachment_approval_failed", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, contract.RepoHeadWriteResult{LocalPath: approved.LocalPath, Path: path, State: "attaching", OperationID: approved.OperationID})
}

// handleHeadFill deepens a sparse copy to the whole tree in place: no picker,
// no second working copy (§1a). A repository without any copy uses the
// ordinary Connect instead.
func (s *Server) handleHeadFill(req contract.Request) contract.Response {
	var p contract.RepoHeadFillPayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	repo, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	if !summary.Attached || !filepath.IsAbs(summary.LocalPath) {
		return contract.ErrResponse(req.RequestID, "HEAD-2005", "ERROR", "REQUIRE_ACTION", "head.anchor_required", nil)
	}
	if repo.Snapshot().Sparse {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := repo.SetDepth(ctx, ".", "infinity"); err != nil {
			if errors.Is(err, ErrDepthUnavailable) {
				return contract.ErrResponse(req.RequestID, "HEAD-2008", "ERROR", "RETRY", "head.copy_not_running", nil)
			}
			return contract.ErrResponse(req.RequestID, "HEAD-2007", "ERROR", "RETRY", "head.fill_failed", map[string]string{"detail": err.Error()})
		}
		if recorder, ok := s.repositoryLifecycleService().(FullDepthRecorder); ok {
			if err := recorder.MarkFullDepth(p.ServerID, p.RepoID); err != nil {
				s.lg.Warnf("head fill: record full depth for %s/%s: %v", p.ServerID, p.RepoID, err)
			}
		}
		repo.SetSparse(false)
	}
	return contract.OKResponse(req.RequestID, contract.RepoHeadWriteResult{LocalPath: summary.LocalPath, Path: ".", State: "attached"})
}
