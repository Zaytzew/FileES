//go:build openbsd

package main

import (
	"filees/internal/obsandbox"
	"filees/pkg/clientprofile"
)

func lockDumpSandbox(opt dumpFlags, profile clientprofile.Profile) error {
	sandbox, err := dumpSandboxProfile(opt, profile)
	if err != nil {
		return err
	}
	return obsandbox.ApplyForExec(sandbox, publishChildPromises)
}
