package ipcserver

import (
	"strings"

	contract "filees/pkg/contract/v1"
)

// AnchorAttacher is the lifecycle's Explorer anchor entry point: an empty
// sparse copy, seeded with placeholders afterwards. Optional on
// RepositoryLifecycleService, like SparseAttacher.
type AnchorAttacher interface {
	BeginAnchorAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error)
}

// AnchorPrecheck refuses a folder that cannot be an anchor on this machine:
// no Cloud Files helper, or a folder inside one another provider already
// synchronises (Windows does not nest sync roots). The daemon supplies it only
// where anchors exist at all, and its presence is the capability.
type AnchorPrecheck func(localPath string) error

func (s *Server) SetAnchorPrecheck(check AnchorPrecheck) {
	s.mu.Lock()
	s.anchorCheck = check
	s.mu.Unlock()
}

// SetPartialAnchorMode makes Explorer anchors this daemon's only kind of
// partial attachment (Windows builds with the Cloud Files API). A plain sparse
// copy is then refused (HEAD-2012), also when the helper is missing.
func (s *Server) SetPartialAnchorMode(anchor bool) {
	s.mu.Lock()
	s.partialAnchor = anchor
	s.mu.Unlock()
}

func (s *Server) partialAnchorMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.partialAnchor
}

func (s *Server) anchorPrecheck() AnchorPrecheck {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.anchorCheck
}

// handleAnchorCreate makes a folder the Explorer anchor of a repository that
// has no working copy here yet. The same trust gate as the HEAD browser, then
// the ordinary attach lifecycle with nothing chosen: the anchor manager seeds
// placeholders once the copy exists, and every path arrives when opened.
func (s *Server) handleAnchorCreate(req contract.Request) contract.Response {
	var p contract.RepoAnchorCreatePayload
	if err := contract.DecodePayload(req.Payload, &p); err != nil || p.ServerID == "" || p.RepoID == "" {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	check := s.anchorPrecheck()
	if check == nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2009", "ERROR", "NONE", "head.anchor_unavailable", nil)
	}
	repo, summary, refusal, ok := s.headRepo(req.RequestID, p.ServerID, p.RepoID)
	if !ok {
		return refusal
	}
	if summary.Attached {
		return contract.ErrResponse(req.RequestID, "HEAD-2010", "ERROR", "NONE", "head.anchor_attached", nil)
	}
	local := strings.TrimSpace(p.LocalPath)
	if local == "" {
		return contract.ErrResponse(req.RequestID, "HEAD-2005", "ERROR", "REQUIRE_ACTION", "head.anchor_required", nil)
	}
	if err := check(local); err != nil {
		return contract.ErrResponse(req.RequestID, "HEAD-2011", "ERROR", "REQUIRE_ACTION", "head.anchor_refused", map[string]string{"detail": err.Error()})
	}
	service := s.repositoryLifecycleService()
	anchor, canAnchor := service.(AnchorAttacher)
	if service == nil || !canAnchor {
		return contract.ErrResponse(req.RequestID, "REPO-0001", "ERROR", "RETRY", "repo.lifecycle_unavailable", nil)
	}
	if repo.ProjectedState() != contract.StateActive {
		return contract.ErrResponse(req.RequestID, "REPO-2004", "ERROR", "RETRY", "repo.not_attachable", nil)
	}
	begun, err := anchor.BeginAnchorAttach(p.ServerID, p.RepoID, local, summary.AttachmentPolicy == "required")
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2002", "ERROR", "REQUIRE_ACTION", "repo.invalid_local_intent", map[string]string{"detail": err.Error()})
	}
	approved, err := service.ApproveAttach(begun.OperationID, p.ServerID, p.RepoID, summary.URL, summary.Access)
	if err != nil {
		return contract.ErrResponse(req.RequestID, "REPO-2005", "ERROR", "REQUIRE_ACTION", "repo.attachment_approval_failed", map[string]string{"detail": err.Error()})
	}
	return contract.OKResponse(req.RequestID, contract.RepoHeadWriteResult{LocalPath: approved.LocalPath, State: "attaching", OperationID: approved.OperationID})
}
