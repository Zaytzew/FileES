package uploadworker

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows exercises accounting in unit tests; the public server is Unix.
// POSIX mode bits cannot verify a Windows DACL. This is a kernel lifetime
// lock, not a claim of a supported Windows server filesystem boundary.
func ownTrashRoot(root string) (io.Closer, error) {
	f, err := os.OpenFile(filepath.Join(root, ".maintenance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
