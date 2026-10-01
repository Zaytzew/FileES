//go:build windows

package predecessor

import (
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

const StorePackageFamily = "FileES.FileESDesktop_wwd47yhb318sj"

var getPackagesByFamily = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetPackagesByPackageFamily")

// StorePresent checks registration for the current user without a shell.
// Removal remains a separate, explicitly confirmed operation.
func StorePresent() (bool, error) {
	family, err := windows.UTF16PtrFromString(StorePackageFamily)
	if err != nil {
		return false, err
	}
	var count, length uint32
	ret, _, _ := getPackagesByFamily.Call(uintptr(unsafe.Pointer(family)), uintptr(unsafe.Pointer(&count)), 0, uintptr(unsafe.Pointer(&length)), 0)
	if ret != 0 && ret != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
		return false, fmt.Errorf("GetPackagesByPackageFamily: %w", windows.Errno(ret))
	}
	return count > 0, nil
}
