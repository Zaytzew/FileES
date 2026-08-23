//go:build !windows

package tray

// WailsPlatformIcons uses the native platform images outside Windows.
func WailsPlatformIcons() IconSet { return PlatformIcons() }
