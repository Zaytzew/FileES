//go:build !windows

package fsdurable

import "os"

// SyncDir flushes a directory entry so a rename or link just performed
// survives a crash.
func SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
