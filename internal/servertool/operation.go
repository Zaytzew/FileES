package servertool

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
)

func RunOperation(args []string, stdout, stderr io.Writer) int {
	path, args, err := configPath(args)
	if err != nil || len(args) == 0 {
		fmt.Fprintln(stderr, "usage: filees-operation [-config path] recover | inspect --operation-id UUID")
		return ExitUsage
	}
	switch args[0] {
	case "recover":
		if len(args) != 1 {
			return ExitUsage
		}
		files, config, err := openFiles(path, toolAccess{name: "filees-operation/recover", areas: onboarding.AreaAll, write: true, needOTP: true, needActivation: true, needRepoResults: true})
		if err != nil {
			report(stderr, "filees-operation config", err)
			return ExitConfig
		}
		if err := files.Recover(); err != nil {
			report(stderr, "filees-operation recover", err)
			return ExitTempFail
		}
		manager, err := activation.New(config.Activation, nil)
		if err != nil {
			report(stderr, "filees-operation activation", err)
			return ExitConfig
		}
		expired, err := manager.CleanupExpired()
		if err != nil {
			report(stderr, "filees-operation activation cleanup", err)
			return ExitTempFail
		}
		orphanedSessions, err := manager.CleanupOrphanedSessionLeases()
		if err != nil {
			report(stderr, "filees-operation session lease cleanup", err)
			return ExitTempFail
		}
		result := map[string]any{
			"schema":                          "filees.operation-result/v1",
			"status":                          "ok",
			"expired_activations":             expired,
			"orphaned_session_leases_removed": orphanedSessions,
		}
		if root := config.Repositories.ResultsRoot; root != "" {
			resultStore, err := repoworker.NewFileStore(filepath.Join(root, "results"))
			if err != nil {
				report(stderr, "filees-operation repository results", err)
				return ExitConfig
			}
			purged, err := resultStore.PurgeExpiredMobilePairings(time.Now())
			if err != nil {
				report(stderr, "filees-operation mobile pairing purge", err)
				return ExitTempFail
			}
			result["purged_mobile_pairings"] = purged
		}
		if err := writeJSON(stdout, result); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "inspect":
		flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
		flags.SetOutput(stderr)
		operationID := flags.String("operation-id", "", "operation UUID")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		files, _, err := openFiles(path, toolAccess{name: "filees-operation/inspect", areas: onboarding.AreaOperations})
		if err != nil {
			report(stderr, "filees-operation config", err)
			return ExitConfig
		}
		operation, err := files.GetOperation(*operationID)
		if err != nil {
			report(stderr, "filees-operation", err)
			return ExitData
		}
		if err := writeJSON(stdout, operation); err != nil {
			return ExitSoftware
		}
		return ExitOK
	default:
		return ExitUsage
	}
}
