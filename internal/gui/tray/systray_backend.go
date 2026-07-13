//go:build cgo && (linux || windows)

package tray

import "fyne.io/systray"

// SystrayBackend is the production adapter for fyne.io/systray. Keeping the
// package-level API behind Backend makes menu rendering deterministic in tests.
type SystrayBackend struct{}

// NewSystrayBackend constructs the platform tray backend.
func NewSystrayBackend() (Backend, error) { return &SystrayBackend{}, nil }

func (*SystrayBackend) Run(onReady, onExit func()) { systray.Run(onReady, onExit) }
func (*SystrayBackend) Quit()                      { systray.Quit() }
func (*SystrayBackend) ResetMenu()                 { systray.ResetMenu() }
func (*SystrayBackend) SetIcon(icon []byte)        { systray.SetIcon(icon) }
func (*SystrayBackend) SetTitle(title string)      { systray.SetTitle(title) }
func (*SystrayBackend) SetTooltip(tooltip string)  { systray.SetTooltip(tooltip) }
func (*SystrayBackend) AddSeparator()              { systray.AddSeparator() }
func (*SystrayBackend) AddMenuItem(title, tooltip string) ItemHandle {
	return systrayItem{item: systray.AddMenuItem(title, tooltip)}
}

type systrayItem struct {
	item *systray.MenuItem
}

func (i systrayItem) AddSubMenuItem(title, tooltip string) ItemHandle {
	return systrayItem{item: i.item.AddSubMenuItem(title, tooltip)}
}
func (i systrayItem) AddSeparator()            { i.item.AddSeparator() }
func (i systrayItem) Disable()                 { i.item.Disable() }
func (i systrayItem) Clicked() <-chan struct{} { return i.item.ClickedCh }

var _ Backend = (*SystrayBackend)(nil)
var _ ItemHandle = systrayItem{}
