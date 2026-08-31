package app

import contract "filees/pkg/contract/v1"

// LockReleaseRequest is the Go-only presentation record. ObservedLockID is
// deliberately retained here for fencing daemon actions, but Wails projects
// neither it nor any client identity into JavaScript.
type LockReleaseRequest struct {
	ID, ServerID, RepoID, Path, ObservedLockID string
	Role, CounterpartyRealmAlias, State        string
	CreatedAt, UpdatedAt, ExpiresAt            string
}

func projectLockReleaseRequest(request contract.LockReleaseRequest) LockReleaseRequest {
	return LockReleaseRequest{
		ID: request.RequestID, ServerID: request.ServerID, RepoID: request.RepoID,
		Path: request.Path, ObservedLockID: request.ObservedLockID,
		Role: request.Role, CounterpartyRealmAlias: request.CounterpartyRealmAlias,
		State: request.State, CreatedAt: request.CreatedAt, UpdatedAt: request.UpdatedAt,
		ExpiresAt: request.ExpiresAt,
	}
}

type LockReleaseCreateRequest struct {
	ServerID, RepoID, Path, ObservedLockID string
}

type LockReleaseDecisionRequest struct {
	ServerID, RequestID string
}
