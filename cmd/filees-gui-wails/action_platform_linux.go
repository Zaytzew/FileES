//go:build linux

package main

import "filees/internal/gui/platform"

func newActionPlatform() platform.Backend {
	return platform.NewLinuxBackend()
}
