package servertool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filees/internal/storagewatch"
	"filees/pkg/activation"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

// No client payload, repository owner or environment variable supplies this
// authority. Local admin routing must preserve the service-owner UID check;
// there is intentionally no user control ticket or GUI action for it.
func localArchiveAdministrator(serviceWC string) func(context.Context, string, repoworker.RepositoryArchivePolicy) (string, error) {
	return func(ctx context.Context, _ string, _ repoworker.RepositoryArchivePolicy) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !repoworker.FileLocksSupported() {
			return "", errors.New("local archive administration requires a Unix server")
		}
		if err := repoworker.CheckServiceWorkingCopyOwner(serviceWC); err != nil {
			return "", err
		}
		return fmt.Sprintf("local-service-owner:uid:%d", os.Geteuid()), nil
	}
}

// releaseRepositoryArchive is a server-internal, local-admin recovery adapter,
// not yet exposed as an administrative command. The shared writer fence is
// bypassed only to acquire the existing exclusive gate for this bound action.
func releaseRepositoryArchive(ctx context.Context, c serverconfig.Config, executable string, req repoworker.ArchiveReleaseRequest) (repoworker.RepositoryArchivePolicy, error) {
	var result repoworker.RepositoryArchivePolicy
	administrator := localArchiveAdministrator(c.Activation.ServiceWorkingCopy)
	if _, err := administrator(ctx, "", repoworker.RepositoryArchivePolicy{}); err != nil {
		return result, err
	}
	manager, err := activation.New(c.Activation, nil)
	if err != nil {
		return result, err
	}
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
				service := repoworker.ArchiveReleaseService{
					ArchiveFreezeService: repoworker.ArchiveFreezeService{
						Authority:  repoworker.ServicePublisher{ServiceWC: c.Activation.ServiceWorkingCopy, DataAuthzFile: c.Repositories.DataAuthzFile, Runner: repoworker.SVNPublishRunner{SVN: c.Activation.SVNBinary, WorkingCopy: c.Activation.ServiceWorkingCopy}},
						Protection: repoworker.ArchiveProtection{Repository: filepath.Join(c.Repositories.Root, req.RepoID), Executable: executable, SVNLook: c.Repositories.EffectiveSVNLookBinary(), SVNAdmin: c.Repositories.SVNAdminBinary},
						Fence:      repoworker.ArchiveAdmissionFence{Root: c.Activation.Root}, WithSessionsStopped: manager.WithAllSessionsStopped,
					}, Administrator: administrator,
				}
				result, err = service.Release(ctx, req)
				return err
			})
		})
	})
	return result, err
}
