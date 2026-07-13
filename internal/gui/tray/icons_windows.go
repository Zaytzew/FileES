//go:build windows

package tray

import (
	_ "embed"

	app "filees/internal/gui/app"
)

var (
	//go:embed assets/windows/active.ico
	windowsActive []byte
	//go:embed assets/windows/busy.ico
	windowsBusy []byte
	//go:embed assets/windows/offline.ico
	windowsOffline []byte
	//go:embed assets/windows/error.ico
	windowsError []byte
	//go:embed assets/windows/disconnected.ico
	windowsDisconnected []byte
)

// PlatformIcons returns embedded multi-resolution ICO tray icons for Windows.
func PlatformIcons() IconSet {
	return IconSet{
		app.IconActive:       windowsActive,
		app.IconBusy:         windowsBusy,
		app.IconOffline:      windowsOffline,
		app.IconError:        windowsError,
		app.IconDisconnected: windowsDisconnected,
	}
}
