package servertool

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/onboarding"
	"filees/pkg/passport"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func RunLockGuardMode(program string, args []string, out, stderr io.Writer) (bool, int) {
	name := filepath.Base(program)
	version := len(args) == 1 && args[0] == "--lock-guard-version"
	if name != "pre-lock" && name != "pre-unlock" && !version {
		return false, 0
	}
	journal := false
	if name == "pre-lock" && len(args) == 5 && args[4] == "0" {
		meta, ok := passport.ParseComment(args[3])
		journal = ok && meta.AcquisitionID != ""
	}
	promises := "stdio"
	if journal {
		promises = "stdio rpath wpath cpath fattr flock"
	}
	if err := sandboxNarrow(promises); err != nil {
		report(stderr, "lock guard sandbox", err)
		return true, ExitSoftware
	}
	if version {
		fmt.Fprintln(out, repoworker.LockGuardVersion)
		return true, ExitOK
	}
	if journal {
		parent := os.Getppid()
		if err := repoworker.AdmitPassportExecution(args, parent, time.Now().UTC()); err != nil {
			report(stderr, "lock acquisition admission", err)
			return true, ExitTempFail
		}
		if parent != os.Getppid() {
			fmt.Fprintln(stderr, "FileES: SVN executor changed during admission")
			return true, ExitTempFail
		}
	}
	return true, repoworker.RunLockGuard(args, stderr)
}

func runAdminLockGuards(configPath string, args []string, out, stderr io.Writer) int {
	flags := flag.NewFlagSet("repo lock-guards", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repoID := flags.String("repo-id", "", "canonical repository UUID")
	realmID := flags.String("owner-realm-id", "", "expected canonical owner realm UUID")
	apply := flags.Bool("apply", false, "install missing guards; never overwrite existing hooks")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return adminUsage(stderr, flags, "--repo-id UUID --owner-realm-id UUID [--apply]")
	}
	for _, id := range []string{*repoID, *realmID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			return adminUsage(stderr, flags, "--repo-id UUID --owner-realm-id UUID [--apply]")
		}
	}
	_, config, err := openFiles(configPath, toolAccess{name: "filees-admin/repo-lock-guards", areas: onboarding.AreaOperations, write: *apply, needRepoResults: true, needRepositoryData: true, needSVN: true, needLockGuards: *apply})
	if err != nil {
		report(stderr, "lock guards config", err)
		return ExitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var states []repoworker.LockGuardStatus
	err = repoworker.WithFileLock(filepath.Join(config.Repositories.ResultsRoot, ".worker.lock"), func() error {
		return withServiceWorkingCopy(ctx, config.Activation, func() error {
			var err error
			states, err = repositoryLockGuards(config, *repoID, *realmID, *apply, repositoryWorkerPath)
			return err
		})
	})
	if err != nil {
		report(stderr, "lock guards", err)
		return ExitTempFail
	}
	if err := writeJSON(out, map[string]any{"schema": "filees.admin-lock-guards/v1", "repo_id": *repoID, "applied": *apply, "hooks": states}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

func repositoryLockGuards(config serverconfig.Config, repoID, ownerRealmID string, apply bool, executable string) ([]repoworker.LockGuardStatus, error) {
	for _, id := range []string{repoID, ownerRealmID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			return nil, errors.New("lock guard scope must use canonical UUIDs")
		}
	}
	if !filepath.IsAbs(config.Repositories.Root) || !filepath.IsAbs(config.Activation.ServiceWorkingCopy) {
		return nil, errors.New("lock guard authority roots must be absolute")
	}
	state, err := readCanonicalRepositoryState(config.Activation.ServiceWorkingCopy, repoID, ownerRealmID)
	if err != nil {
		return nil, err
	}
	if state != "active" && state != "initializing" {
		return nil, errors.New("lock guards require an active or initializing canonical repository")
	}
	repository := filepath.Join(config.Repositories.Root, repoID)
	if apply {
		if err := repoworker.InstallLockGuards(repository, executable); err != nil {
			return nil, err
		}
	}
	return repoworker.InspectLockGuards(repository, executable)
}
