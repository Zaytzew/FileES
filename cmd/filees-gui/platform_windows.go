//go:build windows

package main

import (
	"filees/internal/gui/identity"
	"filees/internal/gui/platform"
)

func newPlatformBackend() (platform.Backend, error) {
	return platform.NewWindowsBackend(platform.WindowsOptions{AUMID: identity.AUMID}), nil
}
