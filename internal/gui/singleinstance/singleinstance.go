// Package singleinstance provides the per-user process lock for filees-gui.
package singleinstance

import "errors"

// ErrAlreadyRunning reports that another GUI instance owns the user lock.
var ErrAlreadyRunning = errors.New("filees-gui is already running")

// Lock keeps the platform lock alive until Close is called.
type Lock interface {
	Close() error
}
