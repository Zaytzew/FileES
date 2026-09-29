//go:build !linux && !windows

package main

import (
	"errors"
	"filees/internal/clientupdate"

	"filees/pkg/config"
	"filees/pkg/ipcserver"
)

func configureClientUpdate(_ *ipcserver.Server, update *config.UpdateConfig, _ bool, _ string, _ clientupdate.AnchorRetirementGuard) error {
	if update != nil {
		return errors.New("client self-update is not implemented on this platform")
	}
	return nil
}
