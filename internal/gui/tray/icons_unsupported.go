//go:build !linux && !windows

package tray

// PlatformIcons returns no icons on targets without a production backend.
func PlatformIcons() IconSet { return nil }
