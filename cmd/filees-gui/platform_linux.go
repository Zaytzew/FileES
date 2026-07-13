//go:build linux

package main

import "filees/internal/gui/platform"

func newPlatformBackend() (platform.Backend, error) {
	return platform.NewLinuxBackend(), nil
}
