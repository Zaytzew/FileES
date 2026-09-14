//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Set only on the Store-specific binary at link time. The package-identity
// check above is independent of this marker, so repackaging an MSI binary
// cannot accidentally enable its in-place updater inside WindowsApps.
var injectedClientUpdateMode string

var getCurrentPackageFullName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentPackageFullName")

func windowsPackageIdentityPresent() (bool, error) {
	var length uint32
	result, _, callErr := getCurrentPackageFullName.Call(uintptr(unsafe.Pointer(&length)), 0)
	switch result {
	case 122: // ERROR_INSUFFICIENT_BUFFER: the process has package identity.
		return true, nil
	case 15700: // APPMODEL_ERROR_NO_PACKAGE.
		return false, nil
	default:
		return false, fmt.Errorf("GetCurrentPackageFullName returned %d: %w", result, callErr)
	}
}
