//go:build windows

package deploy

// syncDir is a no-op on Windows. os.Open opens directories read-only there,
// and FlushFileBuffers on that handle fails with ERROR_ACCESS_DENIED - there
// is no portable way to reopen it for write through the standard os package.
// NTFS journals metadata itself, so the rename/link a caller just performed
// is durable without an explicit parent-directory flush; this differs from
// the POSIX/ext4-style guarantee the call was originally written for, but
// matches common practice for this exact pattern (e.g. bbolt, etcd) on
// Windows.
func syncDir(path string) error {
	return nil
}
