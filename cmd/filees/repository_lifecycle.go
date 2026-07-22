package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"filees/pkg/clientprofile"
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

type attachmentRequest struct {
	OperationID, ServerID, RepoID, RepoURL, Access string
}

func (service repositoryLifecycleService) BeginCreate(serverID, displayName, localPath string) (contract.RepoLifecycleResult, error) {
	if service.provisioning == nil {
		record, err := service.store.BeginCreate(serverID, displayName, localPath)
		return lifecycleResult(record), err
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

func (service repositoryLifecycleService) allRoots() []string {
	roots := append([]string{}, service.existingRoots...)
	if service.store != nil {
		for _, record := range service.store.List() {
			roots = append(roots, record.LocalPath)
		}
	}
	return roots
}

func lifecycleResult(record localrepo.Record) contract.RepoLifecycleResult {
	return contract.RepoLifecycleResult{OperationID: record.OperationID, ServerID: record.ServerID, RepoID: record.RepoID, LocalPath: record.LocalPath, PendingLocalPath: record.PendingLocalPath, State: string(record.State)}
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
