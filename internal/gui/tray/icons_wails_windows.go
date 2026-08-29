//go:build windows

package tray

import (
	_ "embed"

	app "filees/internal/gui/app"
)

var (
	// Wails v3 currently fails to turn the multi-resolution ICO files used by
	// fyne/systray into HICON handles. PNG keeps the same FileES artwork and
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

// WailsPlatformIcons returns renderer-specific tray images. Keep this separate
// from PlatformIcons: the legacy systray backend correctly requires ICO files.
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
