//go:build linux || windows

package tray

import (
	"testing"

	app "filees/internal/gui/app"
)

func TestPlatformIconsContainsEveryState(t *testing.T) {
	icons := PlatformIcons()
	for _, state := range []app.IconState{
		app.IconActive, app.IconBusy, app.IconOffline, app.IconError, app.IconDisconnected,
	} {
		if len(icons[state]) == 0 {
			t.Errorf("missing embedded icon for %q", state)
		}
	}
}
