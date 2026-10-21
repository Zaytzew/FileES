package servertool

import (
	"context"
	"path/filepath"

	"filees/internal/storagewatch"
	"filees/pkg/activation"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

// freezeRepositoryArchive is internal only; no public control/admin command
// invokes it yet. Configuration and executable are server-owned. The caller
// must authenticate realm and bind the planned native UUID/HEAD before calling.
// Lock order is the existing capacity exclusion -> worker -> service WC ->
// activation session barrier. A pending durable archive fence does not prevent
// this recovery path, unlike ordinary shared writer admission.
func freezeRepositoryArchive(ctx context.Context, c serverconfig.Config, executable, realm, repo, operation, repositoryUUID string, revision int64) (repoworker.RepositoryArchivePolicy, error) {
	var result repoworker.RepositoryArchivePolicy
	// Do not disturb any sessions for a foreign repository. The coordinator
	// repeats this check under the service-WC lock before policy effects.
	if err := (repoworker.ServicePublisher{ServiceWC: c.Activation.ServiceWorkingCopy}).OwnsActiveRepository(realm, repo); err != nil {
		return result, err
	}
	manager, err := activation.New(c.Activation, nil)
	if err != nil {
		return result, err
	}
	// Whale sessions hold shared writer leases themselves. Request their end
	// before waiting on the exclusive lease; the barrier below still requires
	// acknowledged exit and revalidates HEAD. Timeout performs no policy cutover.
	if err := manager.SignalAllSessions(ctx); err != nil {
		return result, err
	}
	err = (storagewatch.Gate{Root: c.Activation.Root}).WithExclusive(ctx, func() error {
		return repoworker.WithFileLockContext(ctx, filepath.Join(c.Repositories.ResultsRoot, ".worker.lock"), func() error {
			return withServiceWorkingCopy(ctx, c.Activation, func() error {
				manager, err := activation.NewUnderServiceWorkingCopyLock(c.Activation, nil)
				if err != nil {
					return err
				}
				service := repoworker.ArchiveFreezeService{
					Authority:           repoworker.ServicePublisher{ServiceWC: c.Activation.ServiceWorkingCopy, DataAuthzFile: c.Repositories.DataAuthzFile, Runner: repoworker.SVNPublishRunner{SVN: c.Activation.SVNBinary, WorkingCopy: c.Activation.ServiceWorkingCopy}},
					Protection:          repoworker.ArchiveProtection{Repository: filepath.Join(c.Repositories.Root, repo), Executable: executable, SVNLook: c.Repositories.EffectiveSVNLookBinary(), SVNAdmin: c.Repositories.SVNAdminBinary},
					Fence:               repoworker.ArchiveAdmissionFence{Root: c.Activation.Root},
					WithSessionsStopped: manager.WithAllSessionsStopped,
				}
				result, err = service.Freeze(ctx, realm, repo, operation, repositoryUUID, revision)
				return err
			})
		})
	})
	return result, err
}
