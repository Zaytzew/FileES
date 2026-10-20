//go:build windows && !nocfapi

package main

import (
	"golang.org/x/sys/windows"
	"os"
)

func anchorOpenMaterialization(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Attributes-only handles do not enforce deny-write sharing on Windows.
	// Request read access to this PRIVATE downloaded file, but never read it.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		_ = f.Close()
		if err == nil {
			err = os.ErrInvalid
		}
		return nil, err
	}
	return f, nil
}
