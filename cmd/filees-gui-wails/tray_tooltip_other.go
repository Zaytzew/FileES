//go:build !linux

package main

// Wails implements the native setter on Windows and macOS.
func publishNativeTrayTooltip(string) error { return nil }
