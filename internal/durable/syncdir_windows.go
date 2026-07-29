//go:build windows

package durable

// SyncDirectory is intentionally a no-op on Windows. Calling
// FlushFileBuffers through os.File.Sync on a read-only directory handle
// returns access denied there; NTFS journals the metadata update by Rename.
func SyncDirectory(path string) error { return nil }
