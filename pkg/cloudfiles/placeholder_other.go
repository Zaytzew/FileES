//go:build !windows

package cloudfiles

import "os"

// Cloud Files placeholders exist only on Windows.
func IsPlaceholder(string) bool { return false }

// NotOnDisk is false where there are no placeholders.
func NotOnDisk(string) bool { return false }

// IsSyncRoot is false where there are no placeholders.
func IsSyncRoot(string) bool { return false }

// RecallsOnRead is false where there are no placeholders.
func RecallsOnRead(os.FileInfo) bool { return false }
