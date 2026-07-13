package tray

import app "filees/internal/gui/app"

// Backend isolates the package-level systray API from deterministic renderer
// tests. Implementations must permit calls from goroutines.
type Backend interface {
	Run(onReady, onExit func())
	Quit()
	ResetMenu()
	SetIcon([]byte)
	SetTitle(string)
	SetTooltip(string)
	AddMenuItem(title, tooltip string) ItemHandle
	AddSeparator()
}

// ItemHandle is the minimal mutable menu item surface used by Renderer.
type ItemHandle interface {
	AddSubMenuItem(title, tooltip string) ItemHandle
	AddSeparator()
	Disable()
	Clicked() <-chan struct{}
}

// IconSet contains platform-ready runtime images. Linux uses PNG and Windows
// uses ICO; source SVG files live next to generated assets.
type IconSet map[app.IconState][]byte
