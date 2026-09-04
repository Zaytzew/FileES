//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
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
// applySmallWindowIcon returns what happened, and the caller says it out loud.
//
// The first version swallowed every failure on the grounds that an icon is not
// worth breaking a window over - which is true, and which made it fail in
// silence. It did nothing at all on the owner's machine and nothing anywhere
// reported why; the only symptom was the same blank glyph it was written to
// remove. Not fatal and not silent are different decisions, and this project
// has paid for confusing them more than once.
func applySmallWindowIcon(window *application.WebviewWindow, iconPNG []byte) error {
	if window == nil || len(iconPNG) == 0 {
		return errors.New("no window or no icon")
	}
	handle := window.NativeWindow()
	if handle == nil {
		return errors.New("the window has no native handle yet")
	}
	icon, err := createSmallIcon(iconPNG)
	if err != nil {
		return err
	}
	if _, _, callErr := procSendMessageW.Call(uintptr(handle), wmSetIcon, iconSmall, uintptr(icon)); callErr != nil {
		if errno, ok := callErr.(syscall.Errno); !ok || errno != 0 {
			return fmt.Errorf("WM_SETICON: %w", callErr)
		}
	}
	return nil
}

const (
	wmSetIcon  = 0x0080
	iconSmall  = 0
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
func createSmallIcon(iconPNG []byte) (windows.Handle, error) {
	width, _, _ := procGetSystemMetrics.Call(smCXSMICON)
	height, _, _ := procGetSystemMetrics.Call(smCYSMICON)
	if width == 0 || height == 0 {
		return 0, errors.New("the system reports no small-icon size")
	}
	icon, _, callErr := procCreateIconFromResEx.Call(
		uintptr(unsafe.Pointer(&iconPNG[0])),
		uintptr(len(iconPNG)),
		1, // fIcon: an icon rather than a cursor
		0x00030000,
		width,
		height,
		lrDefaultColor,
	)
	if icon == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx at %dx%d: %w", width, height, callErr)
	}
	return windows.Handle(icon), nil
}
