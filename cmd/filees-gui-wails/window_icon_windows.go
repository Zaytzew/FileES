//go:build windows

package main

import (
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

// applySmallWindowIcon finishes the job Wails starts.
//
// A window carries two icons. Windows uses the large one for the taskbar button
// and the small one for Alt-Tab, the title bar and anywhere else it needs a
// glyph rather than a picture. Wails sets only the large one -
// windowsWebviewWindow.setIcon sends WM_SETICON with ICON_BIG and nothing else -
// so the small icon stays unset and those places fall back to the generic
// default.
//
// Measured after the icon was first wired up: WM_GETICON returned a handle for
// ICON_BIG and zero for ICON_SMALL. That is half a fix, and the half that is
// missing is the one a person sees when they hold Alt-Tab.
//
// Done from here rather than by patching Wails: NativeWindow() hands out the
// HWND for exactly this kind of platform detail, so nothing has to be forked and
// nothing breaks when the dependency moves.
func applySmallWindowIcon(window *application.WebviewWindow, iconPNG []byte) {
	if window == nil || len(iconPNG) == 0 {
		return
	}
	handle := window.NativeWindow()
	if handle == nil {
		return
	}
	icon := createSmallIcon(iconPNG)
	if icon == 0 {
		return
	}
	// Best effort throughout. An interface that refused to open because a
	// decoration could not be applied would be a far worse trade than an
	// Alt-Tab entry with the default glyph.
	_, _, _ = procSendMessageW.Call(uintptr(handle), wmSetIcon, iconSmall, uintptr(icon))
}

const (
	wmSetIcon = 0x0080
	iconSmall = 0
	smCXSMICON = 49
	smCYSMICON = 50
	// LR_DEFAULTCOLOR. The size is given explicitly below, so no size flags.
	lrDefaultColor = 0x00000000
)

var (
	moduleUser32            = windows.NewLazySystemDLL("user32.dll")
	procSendMessageW        = moduleUser32.NewProc("SendMessageW")
	procGetSystemMetrics    = moduleUser32.NewProc("GetSystemMetrics")
	procCreateIconFromResEx = moduleUser32.NewProc("CreateIconFromResourceEx")
)

// createSmallIcon builds an HICON at the system's small-icon size.
//
// The size is asked for rather than assumed: it is 16 logical pixels at 100%
// and larger on a scaled display, and handing Windows a mismatched bitmap gets
// a blurred one back.
func createSmallIcon(iconPNG []byte) windows.Handle {
	width, _, _ := procGetSystemMetrics.Call(smCXSMICON)
	height, _, _ := procGetSystemMetrics.Call(smCYSMICON)
	if width == 0 || height == 0 {
		return 0
	}
	icon, _, _ := procCreateIconFromResEx.Call(
		uintptr(unsafe.Pointer(&iconPNG[0])),
		uintptr(len(iconPNG)),
		1, // fIcon: an icon rather than a cursor
		0x00030000,
		width,
		height,
		lrDefaultColor,
	)
	return windows.Handle(icon)
}
