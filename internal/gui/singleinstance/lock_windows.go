//go:build windows

package singleinstance

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

type mutexLock struct {
	handle windows.Handle
}

// Acquire obtains a named mutex in the current user's local Windows session.
func Acquire(id string) (Lock, error) {
	name, err := windows.UTF16PtrFromString(`Local\` + id)
	if err != nil {
		return nil, fmt.Errorf("single-instance mutex name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(handle)
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("single-instance mutex: %w", err)
	}
	return &mutexLock{handle: handle}, nil
}

func (l *mutexLock) Close() error {
	if l == nil || l.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(l.handle)
	l.handle = 0
	return err
}
