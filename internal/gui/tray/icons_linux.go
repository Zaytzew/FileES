//go:build linux

package tray

import (
	_ "embed"

	app "filees/internal/gui/app"
)

var (
	//go:embed assets/linux/active.png
	linuxActive []byte
	//go:embed assets/linux/busy.png
	linuxBusy []byte
	//go:embed assets/linux/offline.png
	linuxOffline []byte
	//go:embed assets/linux/error.png
	linuxError []byte
	//go:embed assets/linux/shout.png
	linuxShout []byte
	//go:embed assets/linux/disconnected.png
	linuxDisconnected []byte
)

// PlatformIcons returns embedded PNG tray icons for Linux.
func PlatformIcons() IconSet {
	return IconSet{
		app.IconActive:       linuxActive,
		app.IconBusy:         linuxBusy,
		app.IconOffline:      linuxOffline,
		app.IconError:        linuxError,
		app.IconShout:        linuxShout,
		app.IconDisconnected: linuxDisconnected,
	}
}
