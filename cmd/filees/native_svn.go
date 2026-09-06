package main

import (
	"os"
	"runtime"
)

// Explicit Linux alpha deployment opt-in. No PATH discovery and no silent
// disabling when the configured executable disappears: moves must then fail.
func nativeSVNPath() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return os.Getenv("FILEES_NATIVE_SVN")
}
