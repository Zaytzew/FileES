//go:build !cgo || (!linux && !windows)

package tray

import (
	"errors"
	"runtime"
)

// NewSystrayBackend returns a descriptive error on unsupported build targets.
// The pure model and renderer remain buildable and testable without CGO.
func NewSystrayBackend() (Backend, error) {
	return nil, errors.New("system tray unavailable on " + runtime.GOOS + ": requires CGO on Linux or Windows")
}
