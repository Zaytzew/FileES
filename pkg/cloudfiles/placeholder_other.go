//go:build !windows || nocfapi

package cloudfiles

// Provider integration is absent on non-Windows and nocfapi builds.
func IsPlaceholder(string) bool { return false }

// LocalState is unavailable without Cloud Files integration.
func LocalState(string) (bool, bool) { return false, false }

// NotOnDisk is false where there are no placeholders.
func NotOnDisk(string) bool { return false }

// IsSyncRoot is false where there are no placeholders.
func IsSyncRoot(string) bool { return false }
