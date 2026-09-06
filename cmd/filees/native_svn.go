package main

import (
	"os"
	"runtime"
)

// Explicit opt-in. Linux uses the helper only for API gaps (record-move);
// Windows uses it for WC-local verbs too. No PATH discovery and no silent
// disabling when the configured executable disappears.
func nativeSVNPath() string {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return ""
	}
	return os.Getenv("FILEES_NATIVE_SVN")
}
