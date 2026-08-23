//go:build windows

package main

import (
	"filees/internal/gui/identity"
	"filees/internal/gui/platform"
)

func newActionPlatform() platform.Backend {
	return platform.NewWindowsBackend(platform.WindowsOptions{AUMID: identity.AUMID})
}
