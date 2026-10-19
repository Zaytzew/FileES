package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"filees/pkg/clientprofile"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

type repositoryLifecycleService struct {
	localIntentMu          *sync.Mutex
	anchorAdmissionCtx     context.Context
	anchorAdmissionWaiters *atomic.Int32
	anchorAdmissionCheck   func(string, string) error
	deleteAdmissionCheck   func(string, string) error
	anchorEnabled          func(string) bool
	store                  *localrepo.Store
	provisioning           *provisioning.Store
	clientID               func(string) string
	existingRoots          []string
	onCreate               func(string)
	onAttach               func(attachmentRequest)
	onRelocate             func(string)
	onDetach               func(context.Context, string) (localrepo.Record, error)
	onLoadDump             func(string)
	onRepair               func(context.Context, string, string) (localrepo.Record, error)
	onRetryCreate          func(context.Context, localrepo.Record) (localrepo.Record, error)
	// importProgress answers the running initial publication, if any.
	importProgress func(string) *contract.PublishProgress
}

func (service repositoryLifecycleService) BeginRelocate(serverID, repoID, newLocalPath string, moveExisting bool) (contract.RepoLifecycleResult, error) {
	if moveExisting {
		// Queue admission never waits on the execution fence. The attached
		// record lets the current writer/recovery finish before the move.
		record, err := service.store.CurrentLifecycle(serverID, repoID)
		if err != nil {
			return contract.RepoLifecycleResult{}, err
		}
		if record.ConversionID != "" {
			return contract.RepoLifecycleResult{}, errors.New("working copy has a pending conversion")
		}
		if (record.RelocationQueued || record.State == localrepo.StateRelocating) && record.RelocationMoveExisting && record.PendingLocalPath == filepath.Clean(newLocalPath) {
			if service.onRelocate != nil {
				service.onRelocate(record.OperationID)
			}
			return lifecycleResult(record), nil
		}
		check, err := provisioning.PreflightLocalPath(newLocalPath, provisioning.LocalPathAttach, service.allRootsExcept(record.OperationID))
		if err != nil {
			return contract.RepoLifecycleResult{}, err
		}
		if check.Exists {
			return contract.RepoLifecycleResult{}, errors.New("move destination must not exist")
		}
		record, err = service.store.QueueRelocation(serverID, repoID, check.CanonicalPath)
		if err != nil {
			return contract.RepoLifecycleResult{}, err
		}
		if service.onRelocate != nil {
			service.onRelocate(record.OperationID)
		}
		return lifecycleResult(record), nil
	}
	leave, err := service.enterLocalIntent()
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	defer leave()
	if service.fileReleasePending(serverID, repoID) {
		return contract.RepoLifecycleResult{}, service.localMutationPendingError(serverID, repoID)
	}
	check, err := provisioning.PreflightLocalPath(newLocalPath, provisioning.LocalPathAttach, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, err := service.store.BeginRelocation(serverID, repoID, check.CanonicalPath, moveExisting)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if service.onRelocate != nil {
		service.onRelocate(record.OperationID)
	}
	return lifecycleResult(record), nil
}

func (service repositoryLifecycleService) BeginLocate(serverID, repoID, existingLocalPath string) (contract.RepoLifecycleResult, error) {
	leave, err := service.enterLocalIntent()
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	defer leave()
	if service.fileReleasePending(serverID, repoID) {
		return contract.RepoLifecycleResult{}, service.localMutationPendingError(serverID, repoID)
	}
	// The current root may simply have reappeared after reconnecting a drive.
	// Exclude this record only; Store still rejects nested relocation targets.
	current, err := service.store.CurrentLifecycle(serverID, repoID)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	check, err := provisioning.PreflightLocalPath(existingLocalPath, provisioning.LocalPathAttachResume, service.allRootsExcept(current.OperationID))
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, err := service.store.BeginLocate(serverID, repoID, check.CanonicalPath)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if service.onRelocate != nil {
		service.onRelocate(record.OperationID)
	}
	return lifecycleResult(record), nil
}

// BeginLoadDump durably marks repoID as reconciling and enqueues the actual
// work (ticket exchange + WC swap, cmd/filees/repository_provisioner.go
// runReconcile). Options travel through the durable record, not a closure,
// so a daemon restart before the async work runs still has them.
func (service repositoryLifecycleService) BeginLoadDump(serverID, repoID string, applyIgnorePolicy bool, keepLastRevisions *int) (contract.RepoLifecycleResult, error) {
	leave, err := service.enterLocalIntent()
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	defer leave()
	if service.fileReleasePending(serverID, repoID) {
		return contract.RepoLifecycleResult{}, service.localMutationPendingError(serverID, repoID)
	}
	record, err := service.store.BeginReconcile(serverID, repoID, applyIgnorePolicy, keepLastRevisions)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if service.onLoadDump != nil {
		service.onLoadDump(record.OperationID)
	}
	return lifecycleResult(record), nil
}

func (service repositoryLifecycleService) BeginDetach(ctx context.Context, serverID, repoID string, deleteRepository, deleteLocal bool) (contract.RepoLifecycleResult, error) {
	leave, err := service.enterLocalIntent()
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	defer leave()
	if service.fileReleasePending(serverID, repoID) {
		return contract.RepoLifecycleResult{}, service.localMutationPendingError(serverID, repoID)
	}
	if !deleteRepository && service.store.RemoteDeleted(serverID, repoID) {
		record, err := service.store.DismissRemoteDeletedCopy(serverID, repoID)
		return lifecycleResult(record), err
	}
	if deleteLocal && !deleteRepository {
		record, err := service.store.BeginDetachDeletingLocal(serverID, repoID)
		return service.finishDetach(ctx, record, err)
	}
	record, err := service.store.BeginDetach(serverID, repoID, deleteRepository)
	return service.finishDetach(ctx, record, err)
}

func (service repositoryLifecycleService) BeginDelete(ctx context.Context, serverID, repoID, displayName string) (contract.RepoLifecycleResult, error) {
	leave, err := service.waitForDeleteTurn(ctx, serverID, repoID)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	defer leave()
	if service.fileReleasePending(serverID, repoID) {
		return contract.RepoLifecycleResult{}, service.localMutationPendingError(serverID, repoID)
	}
	record, err := service.store.BeginDelete(serverID, repoID, displayName)
	return service.finishDetach(ctx, record, err)
}

func (service repositoryLifecycleService) fileReleasePending(serverID, repoID string) bool {
	for _, record := range service.store.List() {
		if record.ServerID == serverID && record.RepoID == repoID && record.LocalPath != "" && commit.HasPendingLocalMutation(record.LocalPath) {
			return true
		}
	}
	return false
}

func (service repositoryLifecycleService) localMutationPendingError(serverID, repoID string) error {
	for _, record := range service.store.List() {
		if record.ServerID == serverID && record.RepoID == repoID && record.LocalPath != "" && commit.HasPendingLocalMutation(record.LocalPath) {
			return commit.LocalMutationPendingError(record.LocalPath)
		}
	}
	return commit.ErrLocalStatusBusy // Changed state never grants a destructive retry.
}

// Never queue a lifecycle intent behind a long explicit local transfer. The
// shared guard keeps the attached record current through release/recovery;
// the durable pending fence handles restarts and unsupported later builds.
func (service repositoryLifecycleService) enterLocalIntent() (func(), error) {
	if service.localIntentMu == nil {
		return func() {}, nil
	}
	if !service.localIntentMu.TryLock() {
		return nil, commit.ErrLocalStatusBusy
	}
	return service.localIntentMu.Unlock, nil
}

func (service repositoryLifecycleService) DismissRecovery(serverID, repoID, operationID string) (contract.RepoRecoveryDismissResult, error) {
	record, err := service.store.DismissRecovery(serverID, repoID, operationID)
	if err != nil {
		return contract.RepoRecoveryDismissResult{}, err
	}
	return contract.RepoRecoveryDismissResult{
		OperationID: operationID, ServerID: serverID, RepoID: repoID, Dismissed: true,
		LocalCleanupPending: record.State == localrepo.StateDeleting && !record.LocalCleanupCompleted,
	}, nil
}

func (service repositoryLifecycleService) finishDetach(ctx context.Context, record localrepo.Record, err error) (contract.RepoLifecycleResult, error) {
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
		if err := provisioning.CheckInitialContent(record.LocalPath); err != nil {
			return contract.RepoLifecycleResult{}, err
		}
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

func (service repositoryLifecycleService) BeginCreateArchive(serverID, displayName, localPath, archivePath string) (contract.RepoLifecycleResult, error) {
	// Accept and reserve the durable intent, not the execution turn. Store
	// serialises competing root reservations; the provisioner waits for the
	// shared local fence before revalidating and doing any preparation.
	if service.provisioning == nil {
		return contract.RepoLifecycleResult{}, errors.New("archive provisioning is unavailable")
	}
	check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathAttach, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	clientID := ""
	if service.clientID != nil {
		clientID = service.clientID(serverID)
	}
	if clientID == "" {
		return contract.RepoLifecycleResult{}, errors.New("activated client profile is unavailable")
	}
	id := uuid.NewString()
	// Bind the local root first; a staging failure remains an explicit local
	// error, not an enqueued operation with incomplete archive parameters.
	record, err := service.store.BeginCreateOperation(id, serverID, displayName, check.CanonicalPath)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if _, err := service.provisioning.CreateValidatedArchive(id, clientID, check.CanonicalPath, displayName, archivePath); err != nil {
		_, _ = service.store.MarkError(id, err)
		return contract.RepoLifecycleResult{}, err
	}
	if service.onCreate != nil {
		service.onCreate(id)
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
	result := lifecycleResult(record)
	if service.importProgress != nil {
		result.ImportProgress = service.importProgress(operationID)
	}
	return result, nil
}

func (service repositoryLifecycleService) Repair(ctx context.Context, operationID, serverID, repoID, strategy string) (contract.RepoLifecycleResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, ok := service.store.Get(operationID)
	if !ok {
		return contract.RepoLifecycleResult{}, os.ErrNotExist
	}
	if record.ServerID != serverID || record.RepoID != repoID {
		return contract.RepoLifecycleResult{}, errors.New("repository lifecycle repair target does not match the durable operation")
	}
	if strategy != "retry" && strategy != "abandon" {
		return contract.RepoLifecycleResult{}, errors.New("repository lifecycle repair strategy is invalid")
	}
	if service.onRepair == nil {
		return contract.RepoLifecycleResult{}, errors.New("repository lifecycle repair executor is unavailable")
	}
	// Initial-import retry only records a durable intent. Its executor already
	// waits in prepareQueuedCreation; a busy unrelated CFAPI observer must not
	// reject the user's request here. Abandon and other repairs still own the
	// synchronous fence because they may change local metadata immediately.
	if strategy == "retry" && record.State == localrepo.StateRepositoryCreated && service.onRetryCreate != nil {
		if service.fileReleasePending(serverID, repoID) {
			return contract.RepoLifecycleResult{}, service.localMutationPendingError(serverID, repoID)
		}
		record, err := service.onRetryCreate(ctx, record)
		return lifecycleResult(record), err
	}
	leave, err := service.enterLocalIntent()
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	defer leave()
	if service.fileReleasePending(serverID, repoID) {
		return contract.RepoLifecycleResult{}, service.localMutationPendingError(serverID, repoID)
	}
	record, err = service.onRepair(ctx, operationID, strategy)
	return lifecycleResult(record), err
}

func (service repositoryLifecycleService) BeginAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error) {
	check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathAttach, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, err := service.store.BeginAttach(serverID, repoID, check.CanonicalPath, required)
	return lifecycleResult(record), err
}

// BeginSparseAttach starts the unattached browser's working copy: the same
// attach lifecycle, with an empty root and the first chosen path.
func (service repositoryLifecycleService) BeginSparseAttach(serverID, repoID, localPath, sparsePath string, required bool) (contract.RepoLifecycleResult, error) {
	check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathAttach, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, err := service.store.BeginSparseAttach(serverID, repoID, check.CanonicalPath, sparsePath, required)
	return lifecycleResult(record), err
}

// BeginAnchorAttach starts an Explorer anchor through the same attach
// lifecycle: an empty root, nothing chosen, placeholders seeded afterwards by
// the anchor manager.
func (service repositoryLifecycleService) BeginAnchorAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error) {
	parent := service.anchorAdmissionCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	leave, err := service.waitForAnchorTurn(ctx)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	defer leave()
	if service.anchorAdmissionCheck != nil {
		if err := service.anchorAdmissionCheck(serverID, repoID); err != nil {
			return contract.RepoLifecycleResult{}, err
		}
	}
	if service.anchorEnabled != nil && !service.anchorEnabled(serverID) {
		return contract.RepoLifecycleResult{}, errors.New("virtual folders are disabled")
	}
	check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathAttach, service.allRoots())
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	record, err := service.store.BeginAnchorAttach(serverID, repoID, check.CanonicalPath, required)
	return lifecycleResult(record), err
}

// Bounded admission only, within the normal IPC deadline. Observers yield to
// this same creation priority; no transaction, checkout or network is retried.
// All path/activation checks still run AFTER admission, with the fence held.
func (service repositoryLifecycleService) waitForAnchorTurn(ctx context.Context) (func(), error) {
	if service.anchorAdmissionWaiters != nil {
		service.anchorAdmissionWaiters.Add(1)
		defer service.anchorAdmissionWaiters.Add(-1)
	}
	retry := time.NewTicker(25 * time.Millisecond)
	defer retry.Stop()
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, commit.ErrLocalStatusBusy
			}
			return nil, err
		}
		if service.localIntentMu == nil {
			return func() {}, nil
		}
		if service.localIntentMu.TryLock() {
			if err := ctx.Err(); err != nil {
				service.localIntentMu.Unlock()
				return nil, err
			}
			return service.localIntentMu.Unlock, nil
		}
		select {
		case <-ctx.Done():
		case <-retry.C:
		}
	}
}

// MarkFullDepth records that a sparse working copy now holds the whole tree,
// so a later resume treats it as an ordinary attached copy.
func (service repositoryLifecycleService) MarkFullDepth(serverID, repoID string) error {
	_, err := service.store.MarkFullDepth(serverID, repoID)
	return err
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
// repository's target. Records in StateError or StateAbandoned claim no live
// checkout or attachment, so they are excluded;
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
			if record.OperationID == operationID || record.State == localrepo.StateError || record.State == localrepo.StateAbandoned || record.State == localrepo.StateDetached || record.State == localrepo.StateDeleted {
				continue
			}
			// A remote-only deletion (BeginDelete without a working copy) has no
			// root. Passing "" on made the preflight refuse every path on the
			// owner's machine while such a record waited for its retry.
			if record.LocalPath == "" {
				continue
			}
			roots = append(roots, record.LocalPath)
			if record.PendingLocalPath != "" {
				roots = append(roots, record.PendingLocalPath)
			}
		}
	}
	return roots
}

func lifecycleResult(record localrepo.Record) contract.RepoLifecycleResult {
	// Wire state is pending even though the current attachment must remain
	// usable for the writer/recovery which the relocation is waiting on.
	state := lifecycleState(record)
	destination := ""
	if record.ShelfFetch.Placement.ParentRepoID != "" {
		destination = filepath.Join(record.ShelfFetch.Placement.ParentRoot, filepath.FromSlash(record.ShelfFetch.Placement.RelativePath))
	}
	return contract.RepoLifecycleResult{FetchDestination: destination, FetchID: record.ShelfFetch.ID, FetchUploadID: record.ShelfFetch.UploadID, FetchState: record.ShelfFetch.State, FetchError: record.ShelfFetch.Error, OperationID: record.OperationID, ServerID: record.ServerID, RepoID: record.RepoID, LocalPath: record.LocalPath, PendingLocalPath: record.PendingLocalPath, State: state, LastError: record.LastError, LastErrorKey: record.LastErrorKey, ServerDeleteCompleted: record.ServerDeleteCompleted, RetainUntil: record.RetainUntil, RecoveryPrepared: record.RecoveryPrepared, RecoveryKitPath: record.RecoveryKitPath, LocalCleanupCompleted: record.LocalCleanupCompleted}
}

func lifecycleState(record localrepo.Record) string {
	if record.RelocationQueued && record.State == localrepo.StateAttached {
		return contract.LifecycleRelocationQueued
	}
	return string(record.State)
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
		if record.ConversionID != "" && record.State == localrepo.StateRelocating {
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
		repository.Purpose = record.Purpose
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
