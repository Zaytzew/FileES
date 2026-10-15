package main

import (
	"context"
	"slices"
	"time"

	"filees/pkg/errcat"
	"filees/pkg/localrepo"
)

// Wait only for admission, never replay the destructive server operation.
// Share the existing observer priority and leave the original confirmation
// bound to its local attachment. A new attachment/move requires fresh consent.
func (service repositoryLifecycleService) waitForDeleteTurn(ctx context.Context, serverID, repoID string) (func(), error) {
	before := service.deleteBindings(serverID, repoID)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if service.anchorAdmissionCtx != nil {
		stop := context.AfterFunc(service.anchorAdmissionCtx, cancel)
		defer stop()
		if service.anchorAdmissionCtx.Err() != nil {
			cancel()
		}
	}
	leave, err := service.waitForAnchorTurn(waitCtx)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(before, service.deleteBindings(serverID, repoID)) {
		leave()
		return nil, errcat.New("repo.delete_state_changed", nil, nil)
	}
	if service.deleteAdmissionCheck != nil {
		if err := service.deleteAdmissionCheck(serverID, repoID); err != nil {
			leave()
			return nil, err
		}
	}
	if err := waitCtx.Err(); err != nil {
		leave()
		return nil, err
	}
	if service.anchorAdmissionCtx != nil && service.anchorAdmissionCtx.Err() != nil {
		leave()
		return nil, service.anchorAdmissionCtx.Err()
	}
	return leave, nil
}

type deleteBinding struct {
	OperationID, LocalPath, PendingLocalPath string
	State                                    localrepo.State
	Anchor                                   bool
}

func (service repositoryLifecycleService) deleteBindings(serverID, repoID string) []deleteBinding {
	var bindings []deleteBinding
	for _, r := range service.store.List() {
		if r.ServerID == serverID && r.RepoID == repoID {
			bindings = append(bindings, deleteBinding{r.OperationID, r.LocalPath, r.PendingLocalPath, r.State, r.Anchor})
		}
	}
	return bindings
}
