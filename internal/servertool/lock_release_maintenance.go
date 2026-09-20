package servertool

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

func sweepLockReleases(ctx context.Context, config serverconfig.Config) (int, error) {
	r := config.Repositories
	store := &repoworker.FileLockReleaseStore{Root: filepath.Join(r.ResultsRoot, "lock-release-requests")}
	authority := repoworker.SVNAdminLockAuthority{SVNAdmin: r.SVNAdminBinary, RepositoriesRoot: r.Root}
	publisher := repoworker.ServicePublisher{ServiceWC: config.Activation.ServiceWorkingCopy, Runner: repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: config.Activation.ServiceWorkingCopy}}
	return store.Sweep(ctx, authority, publisher)
}

// Scheduled independently from passport expiry: this operation needs the
// service WC and its SVN publication rights, under the usual worker/WC locks.
func runAdminReapLockRequests(configPath string, args []string, out, stderr io.Writer) int {
	if len(args) != 0 {
		return ExitUsage
	}
	_, config, err := openFiles(configPath, toolAccess{name: "filees-admin/reap-lock-requests", areas: onboarding.AreaOperations, write: true, needRepoResults: true, needRepositoryData: true, needSVN: true})
	if err != nil {
		report(stderr, "lock request maintenance config", err)
		return ExitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	removed := 0
	err = repoworker.WithFileLock(filepath.Join(config.Repositories.ResultsRoot, ".worker.lock"), func() error {
		return withServiceWorkingCopy(ctx, config.Activation, func() error { var err error; removed, err = sweepLockReleases(ctx, config); return err })
	})
	if err != nil {
		report(stderr, "lock request maintenance", err)
		return ExitTempFail
	}
	if err := writeJSON(out, map[string]any{"schema": "filees.lock-release-maintenance/v1", "removed": removed}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
