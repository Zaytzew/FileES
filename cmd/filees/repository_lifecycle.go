package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"filees/pkg/clientprofile"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

type repositoryLifecycleService struct {
	store         *localrepo.Store
	provisioning  *provisioning.Store
	clientID      func(string) string
	existingRoots []string
	onCreate      func(string)
	onAttach      func(attachmentRequest)
	onRelocate    func(string)
	onDetach      func(context.Context, string) (localrepo.Record, error)
}

func (service repositoryLifecycleService) BeginRelocate(serverID, repoID, newLocalPath string) (contract.RepoLifecycleResult, error) {
	check, err := provisioning.PreflightLocalPath(newLocalPath, provisioning.LocalPathAttach, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, err := service.store.BeginRelocation(serverID, repoID, check.CanonicalPath)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if service.onRelocate != nil {
		service.onRelocate(record.OperationID)
	}
	return lifecycleResult(record), nil
}

func (service repositoryLifecycleService) BeginDetach(ctx context.Context, serverID, repoID string, deleteRepository bool) (contract.RepoLifecycleResult, error) {
	record, err := service.store.BeginDetach(serverID, repoID, deleteRepository)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if service.onDetach == nil {
		return contract.RepoLifecycleResult{}, errors.New("repository detach executor is unavailable")
	}
	record, err = service.onDetach(ctx, record.OperationID)
	if err != nil {
		if failed, persistErr := service.store.RecordDetachError(record.OperationID, err); persistErr == nil {
			record = failed
		}
	}
	return lifecycleResult(record), err
}

type attachmentRequest struct {
	OperationID, ServerID, RepoID, RepoURL, Access string
}

func (service repositoryLifecycleService) BeginCreate(serverID, displayName, localPath string) (contract.RepoLifecycleResult, error) {
	if service.provisioning == nil {
		record, err := service.store.BeginCreate(serverID, displayName, localPath)
		return lifecycleResult(record), err
	}
	if record, ok, err := service.recoverableCreate(serverID, localPath); err != nil {
		return contract.RepoLifecycleResult{}, err
	} else if ok {
		record, err = service.store.ResumeCreate(record.OperationID)
		if err != nil {
			return contract.RepoLifecycleResult{}, err
		}
		if service.onCreate != nil {
			service.onCreate(record.OperationID)
		}
		return lifecycleResult(record), nil
	}
	check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathCreate, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	var snapshot provisioning.Snapshot
	if _, statErr := os.Stat(check.CanonicalPath); statErr == nil {
		snapshot, err = provisioning.ScanInitialSnapshot(check.CanonicalPath)
		if err != nil {
			return contract.RepoLifecycleResult{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return contract.RepoLifecycleResult{}, statErr
	}
	clientID := ""
	if service.clientID != nil {
		clientID = service.clientID(serverID)
	}
	if clientID == "" {
		return contract.RepoLifecycleResult{}, errors.New("activated client profile is unavailable")
	}
	operationID := uuid.NewString()
	record, err := service.store.BeginCreateOperation(operationID, serverID, displayName, check.CanonicalPath)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if _, err := service.provisioning.CreateValidatedSnapshot(operationID, clientID, check.CanonicalPath, displayName, snapshot.TotalBytes, len(snapshot.Directories)+len(snapshot.Files)); err != nil {
		_, _ = service.store.MarkError(operationID, err)
		return contract.RepoLifecycleResult{}, err
	}
	if service.onCreate != nil {
		service.onCreate(operationID)
	}
	return lifecycleResult(record), nil
}

func (service repositoryLifecycleService) recoverableCreate(serverID, localPath string) (localrepo.Record, bool, error) {
	if service.store == nil {
		return localrepo.Record{}, false, nil
	}
	for _, record := range service.store.List() {
		if record.ServerID != serverID || record.State != localrepo.StateRepositoryCreated {
			continue
		}
		check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathCreateResume, service.allRootsExcept(record.OperationID))
		if err != nil {
			continue
		}
		if check.CanonicalPath == record.LocalPath {
			return record, true, nil
		}
	}
	return localrepo.Record{}, false, nil
}

func (service repositoryLifecycleService) Status(operationID string) (contract.RepoLifecycleResult, error) {
	record, ok := service.store.Get(operationID)
	if !ok {
		return contract.RepoLifecycleResult{}, os.ErrNotExist
	}
	return lifecycleResult(record), nil
}

func (service repositoryLifecycleService) BeginAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error) {
	check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathAttach, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, err := service.store.BeginAttach(serverID, repoID, check.CanonicalPath, required)
	return lifecycleResult(record), err
}

func (service repositoryLifecycleService) ApproveAttach(operationID, serverID, repoID, repoURL, access string) (contract.RepoLifecycleResult, error) {
	if access != "r" && access != "rw" {
		return contract.RepoLifecycleResult{}, errors.New("repository attachment access must be r or rw")
	}
	record, err := service.store.ApproveAttach(operationID, serverID, repoID, repoURL, access)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if service.onAttach != nil {
		service.onAttach(attachmentRequest{OperationID: operationID, ServerID: serverID, RepoID: repoID, RepoURL: repoURL, Access: access})
	}
	return lifecycleResult(record), nil
}

// allRoots lists local paths that must not be nested inside a new
// repository's target. A record in StateError never reached a live
// checkout or attachment — it claims no real resource — so it is excluded;
// otherwise one failed attempt (e.g. a transient STORAGE_INSUFFICIENT that
// later clears server-side) would permanently block every future attempt at
// the same path. Genuine leftover Subversion metadata on disk is still
// caught independently by rejectWorkingCopy's on-disk check.
func (service repositoryLifecycleService) allRoots() []string {
	return service.allRootsExcept("")
}

func (service repositoryLifecycleService) allRootsExcept(operationID string) []string {
	roots := append([]string{}, service.existingRoots...)
	if service.store != nil {
		for _, record := range service.store.List() {
			if record.OperationID == operationID || record.State == localrepo.StateError || record.State == localrepo.StateDetached || record.State == localrepo.StateDeleted {
				continue
			}
			roots = append(roots, record.LocalPath)
		}
	}
	return roots
}

func lifecycleResult(record localrepo.Record) contract.RepoLifecycleResult {
	return contract.RepoLifecycleResult{OperationID: record.OperationID, ServerID: record.ServerID, RepoID: record.RepoID, LocalPath: record.LocalPath, PendingLocalPath: record.PendingLocalPath, State: string(record.State), LastError: record.LastError}
}

func reconcileConfiguredRepositoryLifecycle(store *localrepo.Store, repositories []config.Repo) ([]config.Repo, error) {
	if store == nil {
		return repositories, nil
	}
	active := make([]config.Repo, 0, len(repositories))
	for _, repository := range repositories {
		record, _, err := store.EnsureConfiguredAttached(
			repository.ServerID, repository.ID, repository.RepoURL, repository.Access,
			repository.LocalPath, repository.ID,
		)
		if err != nil {
			return nil, err
		}
		switch record.State {
		case localrepo.StateDetaching, localrepo.StateDeleting, localrepo.StateDetached, localrepo.StateDeleted:
			continue
		}
		if record.RepoURL != "" {
			repository.RepoURL = record.RepoURL
		}
		if record.Access != "" {
			repository.Access = record.Access
		}
		if record.LocalPath != "" {
			repository.LocalPath = record.LocalPath
		}
		active = append(active, repository)
	}
	return active, nil
}

func defaultRepositoryLifecyclePath() string {
	return filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "repository-lifecycle.json")
}

func defaultRepositoryProvisioningPath() string {
	return filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "repository-provisioning")
}

func defaultActivityJournalPath() string {
	return filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "recent-activity.json")
}
