//go:build windows

package tray

import (
	_ "embed"

	app "filees/internal/gui/app"
)

var (
	// PNG keeps the same FileES artwork and
	// status overlays while using Wails' reliable image decoder path.
	//go:embed assets/linux/active.png
	wailsWindowsActive []byte
	//go:embed assets/linux/busy.png
	wailsWindowsBusy []byte
	//go:embed assets/linux/offline.png
	wailsWindowsOffline []byte
	//go:embed assets/linux/error.png
	wailsWindowsError []byte
	//go:embed assets/linux/shout.png
	wailsWindowsShout []byte
	//go:embed assets/linux/disconnected.png
	wailsWindowsDisconnected []byte
)

// WailsPlatformIcons returns PNG tray images for the Wails decoder.
func WailsPlatformIcons() IconSet {
	return IconSet{
		app.IconActive:       wailsWindowsActive,
		app.IconBusy:         wailsWindowsBusy,
		app.IconOffline:      wailsWindowsOffline,
		app.IconError:        wailsWindowsError,
		app.IconShout:        wailsWindowsShout,
		app.IconDisconnected: wailsWindowsDisconnected,
	}
}
