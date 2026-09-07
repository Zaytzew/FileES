package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

// RunPassportReap is one cron invocation, not an SSH/control operation. Every
// invocation has a deadline; cron starts a fresh process after failure/death.
func RunPassportReap(args []string, stdout, stderr io.Writer) int {
	return runPassportReap(args, stdout, stderr, repositoryWorkerPath)
}

func runPassportReap(args []string, stdout, stderr io.Writer, worker string) int {
	path, rest, err := configPath(args)
	check := len(rest) == 1 && rest[0] == "--check"
	if err != nil || (len(rest) != 0 && !check) {
		fmt.Fprintln(stderr, "usage: filees-worker passport-reap [-config path] [--check]")
		return ExitUsage
	}
	if err := sandboxBegin(svnPromises); err != nil {
		report(stderr, "passport maintenance sandbox", err)
		return ExitSoftware
	}
	// No OTP, activation secret, service-WC update or network dependency.
	config, err := serverconfig.LoadFor(path, 0)
	if err != nil {
		report(stderr, "passport maintenance config", err)
		return ExitConfig
	}
	for _, root := range []string{config.Repositories.Root, config.Repositories.ResultsRoot, config.Activation.Root} {
		if err := repoworker.ValidateMaintenanceRoot(root); err != nil {
			report(stderr, "passport maintenance root", err)
			return ExitConfig
		}
	}
	if !filepath.IsAbs(config.Repositories.SVNAdminBinary) {
		return ExitConfig
	}
	statusDir := filepath.Join(config.Repositories.ResultsRoot, "passport-maintenance")
	if !check {
		if err := os.MkdirAll(statusDir, 0700); err != nil {
			report(stderr, "passport maintenance status", err)
			return ExitConfig
		}
	}
	if err := repoworker.ValidateMaintenanceRoot(statusDir); err != nil {
		report(stderr, "passport maintenance status", err)
		return ExitTempFail
	}
	statusFile := filepath.Join(statusDir, "status.json")
	profile := passportMaintenanceProfile(config, statusDir, check, worker)
	if check {
		err = sandboxApply(profile)
	} else {
		err = sandboxApplyForExec(profile, svnHookExecPromises)
	}
	if err != nil {
		report(stderr, "passport maintenance sandbox", err)
		return ExitSoftware
	}
	if check {
		f, err := os.Open(statusFile)
		if err != nil {
			report(stderr, "passport maintenance status", err)
			return ExitTempFail
		}
		defer f.Close()
		var status repoworker.PassportReapStatus
		raw, err := io.ReadAll(io.LimitReader(f, 16385))
		if err == nil && len(raw) > 16384 {
			err = errors.New("oversized maintenance status")
		}
		if err == nil {
			err = json.Unmarshal(raw, &status)
		}
		if err == nil {
			err = repoworker.CheckPassportMaintenance(status, time.Now().UTC(), 5*time.Minute)
		}
		if err != nil {
			report(stderr, "passport maintenance health", err)
			return ExitTempFail
		}
		if err := writeJSON(stdout, status); err != nil {
			return ExitSoftware
		}
		return ExitOK
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	reaper := repoworker.ScheduledPassportReaper{
		Executions: repoworker.PassportExecutions{Authority: repoworker.PassportReplacementAuthority{Locks: repoworker.SVNAdminLockAuthority{
			SVNAdmin: config.Repositories.SVNAdminBinary, RepositoriesRoot: config.Repositories.Root,
		}}},
		WorkerLock:  filepath.Join(config.Repositories.ResultsRoot, ".worker.lock"),
		ServiceLock: filepath.Join(config.Activation.Root, ".service-wc.lock"), StatusFile: statusFile,
	}
	status, err := reaper.Run(ctx)
	err = errors.Join(err, writeJSON(stdout, status))
	if err != nil {
		report(stderr, "passport maintenance", err)
		return ExitTempFail
	}
	return ExitOK
}

func passportMaintenanceProfile(config serverconfig.Config, statusDir string, check bool, worker string) obsandbox.Profile {
	profile := obsandbox.Profile{Name: "filees-worker/passport-reap", Promises: "stdio rpath", Paths: []obsandbox.Path{
		{Label: "maintenance-status", Name: statusDir, Perms: "r"},
	}}
	if check {
		return profile
	}
	profile.Promises = svnPromises
	profile.Paths[0].Perms = "rwc"
	profile.Paths = append(profile.Paths,
		obsandbox.Path{Label: "worker-lock", Name: filepath.Join(config.Repositories.ResultsRoot, ".worker.lock"), Perms: "rwc"},
		obsandbox.Path{Label: "service-lock", Name: filepath.Join(config.Activation.Root, ".service-wc.lock"), Perms: "rwc"},
		obsandbox.Path{Label: "repositories-parent", Name: filepath.Dir(config.Repositories.Root), Perms: "r"},
		obsandbox.Path{Label: "repositories", Name: config.Repositories.Root, Perms: "rwc"},
		obsandbox.Path{Label: "svnadmin", Name: config.Repositories.SVNAdminBinary, Perms: "rx"},
		obsandbox.Path{Label: "guard-worker", Name: worker, Perms: "rx"},
		obsandbox.Path{Label: "null", Name: "/dev/null", Perms: "rw"},
		obsandbox.Path{Label: "random", Name: "/dev/urandom", Perms: "r"},
		obsandbox.Path{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		obsandbox.Path{Label: "hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		obsandbox.Path{Label: "system-libs", Name: "/usr/lib", Perms: "r"},
		obsandbox.Path{Label: "local-libs", Name: "/usr/local/lib", Perms: "r"},
		obsandbox.Path{Label: "svn-config", Name: "/etc/subversion", Perms: "r"},
	)
	return profile
}
