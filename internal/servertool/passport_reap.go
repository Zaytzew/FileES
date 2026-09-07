package servertool

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

// Explicitly scoped maintenance, suitable for an operator-scheduled invocation.
// No scheduler or deployment is installed by adding this command.
func runAdminReapPassports(configPath string, args []string, out, stderr io.Writer) int {
	flags := flag.NewFlagSet("repo reap-passports", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repoID := flags.String("repo-id", "", "canonical repository UUID")
	owner := flags.String("owner-realm-id", "", "expected canonical owner UUID")
	path := flags.String("path", "", "optional relative path for a legacy expired passport")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return adminUsage(stderr, flags, "--repo-id UUID --owner-realm-id UUID [--path relative-path]")
	}
	for _, id := range []string{*repoID, *owner} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return adminUsage(stderr, flags, "--repo-id UUID --owner-realm-id UUID [--path relative-path]")
		}
	}
	_, config, err := openFiles(configPath, toolAccess{name: "filees-admin/reap-passports", areas: onboarding.AreaOperations, write: true, needRepoResults: true, needRepositoryData: true, needSVN: true, needLockGuards: true})
	if err != nil {
		report(stderr, "passport reap config", err)
		return ExitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = repoworker.WithFileLock(filepath.Join(config.Repositories.ResultsRoot, ".worker.lock"), func() error {
		return withServiceWorkingCopy(ctx, config.Activation, func() error {
			state, err := readCanonicalRepositoryState(config.Activation.ServiceWorkingCopy, *repoID, *owner)
			if err != nil {
				return err
			}
			if state != "active" {
				return errors.New("passport reap requires the expected active repository")
			}
			svc := repoworker.PassportExecutions{Authority: repoworker.PassportReplacementAuthority{
				ServiceWC: config.Activation.ServiceWorkingCopy,
				Locks:     repoworker.SVNAdminLockAuthority{SVNAdmin: config.Repositories.SVNAdminBinary, RepositoriesRoot: config.Repositories.Root},
			}}
			if *path != "" {
				return svc.ExpirePath(ctx, *repoID, *path)
			}
			return svc.ReapRepository(ctx, *repoID)
		})
	})
	if err != nil {
		report(stderr, "passport reap", err)
		return ExitTempFail
	}
	if err := writeJSON(out, map[string]any{"schema": "filees.passport-reap/v1", "repo_id": *repoID, "path": *path, "state": "checked"}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
