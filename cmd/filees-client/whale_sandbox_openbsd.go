//go:build openbsd

package main

import (
	"filees/internal/obsandbox"
	"filees/pkg/clientprofile"
)

func lockWhaleSandbox(opt whaleFlags, profile clientprofile.Profile) error {
	sandbox, err := whaleSandboxProfile(opt, profile)
	if err != nil {
		return err
	}
	return obsandbox.Apply(sandbox)
}
