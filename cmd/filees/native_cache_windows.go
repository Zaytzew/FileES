//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var getCurrentPackageFamilyName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentPackageFamilyName")

// nativeCacheRoot is where the embedded native SVN runtime is extracted.
//
// A Store (MSIX) process writing to %LOCALAPPDATA% is redirected into the
// package's private copy, so the daemon saw the extracted filees-svn.exe but
// the loader starting it looked for its DLLs in the real, empty directory:
// "native SVN --version failed: exit status 0xc0000135" on a clean Windows
// Sandbox (2026-09-24), and no projection ever arrived. The package's own
// LocalCache is not redirected - the same path from inside and outside the
// package - so a packaged process extracts there. An MSI keeps the user cache.
func nativeCacheRoot() (string, error) {
	family, err := currentPackageFamilyName()
	if err != nil {
		return "", err
	}
	if family == "" {
		return os.UserCacheDir()
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}
	return filepath.Join(local, "Packages", family, "LocalCache"), nil
}

// currentPackageFamilyName answers "" for a process without package identity.
func currentPackageFamilyName() (string, error) {
	var length uint32
	result, _, _ := getCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&length)), 0)
	switch result {
	case 15700: // APPMODEL_ERROR_NO_PACKAGE
		return "", nil
	case 122: // ERROR_INSUFFICIENT_BUFFER: packaged, length now set
	default:
		return "", fmt.Errorf("GetCurrentPackageFamilyName returned %d", result)
	}
	buffer := make([]uint16, length)
	if result, _, _ = getCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0]))); result != 0 {
		return "", fmt.Errorf("GetCurrentPackageFamilyName returned %d", result)
	}
	return syscall.UTF16ToString(buffer), nil
}
