//go:build !windows

package cloudfiles

import "os"

// RecallsOnRead is unavailable outside Windows.
func RecallsOnRead(os.FileInfo) bool { return false }
