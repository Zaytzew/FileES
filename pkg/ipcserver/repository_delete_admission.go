package ipcserver

import (
	"context"
	"errors"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
)

// ValidateRepositoryDeletion is repeated after waiting for local admission.
// The server still performs its own authoritative authorization.
func (s *Server) ValidateRepositoryDeletion(serverID, repoID string) error {
	rs := s.repoByID(repoID)
	if rs == nil || rs.ServerID() != serverID {
		return errcat.New("proto.repo_not_found", nil, nil)
	}
	summary := rs.Summary()
	if summary.ServerDeleted || summary.AttachmentPolicy == "required" {
		return errcat.New("repo.delete_forbidden", nil, nil)
	}
	s.mu.RLock()
	activation, ok := s.activations[serverID]
	s.mu.RUnlock()
	if !ok || activation.ClientRole == contract.ClientRoleReadOnly || !activation.CanCreateRepositories ||
		activation.RealmID == "" || summary.OwnerRealmID != activation.RealmID {
		return errcat.New("repo.delete_forbidden", nil, nil)
	}
	return nil
}

func repositoryDeleteError(id string, err error) contract.Response {
	var fault errcat.Fault
	if errors.As(err, &fault) {
		return contract.ErrResponseFrom(id, fault)
	}
	key := "repo.delete_failed"
	if errors.Is(err, commit.ErrLocalStatusBusy) {
		key = "repo.delete_busy"
	} else if errors.Is(err, context.Canceled) {
		key = "repo.delete_canceled"
	}
	return contract.ErrResponseFrom(id, errcat.New(errcat.Key(key), map[string]string{"detail": err.Error()}, err))
}
