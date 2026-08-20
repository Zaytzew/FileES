package servertool

import (
	"fmt"
	"io"
	"path/filepath"

	"filees/internal/obsandbox"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

const ownershipCorrectorPromises = "stdio rpath wpath cpath fattr chown flock"

func RunServiceWCOwnershipCorrector(args []string, stdout, stderr io.Writer) int {
	return runServiceWCOwnershipCorrector("/etc/filees/server.json", args, stdout, stderr)
}

func runServiceWCOwnershipCorrector(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "filees-service-wc-corrector: no arguments are accepted")
		return ExitUsage
	}
	if err := sandboxBegin(ownershipCorrectorPromises); err != nil {
		report(stderr, "filees-service-wc-corrector sandbox", err)
		return ExitSoftware
	}
	config, err := serverconfig.LoadFor(configPath, 0)
	if err != nil {
		report(stderr, "filees-service-wc-corrector config", err)
		return ExitConfig
	}
	profile := obsandbox.Profile{
		Name:     "filees-service-wc-corrector",
		Promises: ownershipCorrectorPromises,
		Paths: []obsandbox.Path{
			{Label: "activation-root", Name: config.Activation.Root, Perms: "rwc"},
			{Label: "service-wc", Name: config.Activation.ServiceWorkingCopy, Perms: "rwc"},
		},
	}
	if err := sandboxApply(profile); err != nil {
		report(stderr, "filees-service-wc-corrector sandbox", err)
		return ExitSoftware
	}
	result, err := repoworker.CorrectServiceWorkingCopyOwnership(config.Activation.Root, config.Activation.ServiceWorkingCopy)
	if err != nil {
		report(stderr, "filees-service-wc-corrector", err)
		return ExitData
	}
	if err := writeJSON(stdout, map[string]any{
		"schema": "filees.service-wc-ownership-correction/v1", "status": "ok",
		"working_copy": filepath.Clean(config.Activation.ServiceWorkingCopy),
		"inspected":    result.Inspected, "corrected": result.Corrected,
	}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
