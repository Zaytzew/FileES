//go:build !linux

package main

import (
	"errors"

	"filees/pkg/config"
	"filees/pkg/ipcserver"
)

func configureClientUpdate(_ *ipcserver.Server, update *config.UpdateConfig, _ string) error {
	if update != nil {
		return errors.New("client self-update is not implemented on this platform")
	}
	return nil
}
