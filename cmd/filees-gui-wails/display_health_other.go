//go:build !windows

package main

import (
	"context"
	"filees/internal/gui/platform"
)

// Windows browser disappearance is the observed failure, not a Linux policy.
func watchDisplayHealth(context.Context, string, *GUIService, platform.Backend, func() bool, func()) {
}
