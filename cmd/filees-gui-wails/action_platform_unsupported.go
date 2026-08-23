//go:build !windows && !linux

package main

import "filees/internal/gui/platform"

func newActionPlatform() platform.Backend {
	return nil
}
