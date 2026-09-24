//go:build !windows

package main

import "os"

// nativeCacheRoot is where the embedded native SVN runtime is extracted.
func nativeCacheRoot() (string, error) {
	return os.UserCacheDir()
}
