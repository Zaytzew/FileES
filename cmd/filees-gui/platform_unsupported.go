//go:build !linux && !windows

package main

import (
	"fmt"

	"filees/internal/gui/platform"
)

func newPlatformBackend() (platform.Backend, error) {
	return nil, fmt.Errorf("unsupported operating system")
}
