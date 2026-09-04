//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// applySmallWindowIcon is a no-op outside Windows.
//
// The two-icon split - a large one for the taskbar, a small one for Alt-Tab and
// the title bar - is a Win32 idea. Every other platform takes one icon and
// scales it, which is what application.Options.Icon already provides.
func applySmallWindowIcon(window *application.WebviewWindow, iconPNG []byte) error { return nil }
