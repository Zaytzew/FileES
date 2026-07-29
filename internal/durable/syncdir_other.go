//go:build !windows

// Package durable contains platform-dependent pieces needed by crash-safe
// file replacement.
package durable

import "os"

// SyncDirectory makes a preceding rename durable on filesystems where a
// directory fsync is supported.
func SyncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
