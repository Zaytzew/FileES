//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

type workingCopyGuard windows.Handle

func acquireWorkingCopyGuard(path string) (workingCopyGuard, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// Deliberately omit FILE_SHARE_DELETE. The directory remains fully usable,
	// but Windows refuses an out-of-band rename or removal until FileES stops
	// this repository pipeline and closes the guard.
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("guard working-copy root %s: %w", path, err)
	}
	return workingCopyGuard(handle), nil
}

func (guard workingCopyGuard) Close() error {
	if guard == 0 || windows.Handle(guard) == windows.InvalidHandle {
		return nil
	}
	return windows.CloseHandle(windows.Handle(guard))
}
