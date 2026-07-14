//go:build !linux && !windows

package tray

import (
	"errors"
	"runtime"
)

// NewSystrayBackend returns a descriptive error on unsupported operating systems.
// Linux and Windows implementations are pure Go and do not require CGO.
func NewSystrayBackend() (Backend, error) {
	return nil, errors.New("system tray unavailable on unsupported operating system " + runtime.GOOS)
}
