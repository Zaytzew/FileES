//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || solaris

package repoworker

import (
	"context"
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FilesystemCapacity makes the server filesystem the authority for admission.
type FilesystemCapacity struct{ Root string }

func (c FilesystemCapacity) Check(ctx context.Context, contentBytes int64) (int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if !filepath.IsAbs(c.Root) || contentBytes < 0 {
		return 0, 0, errors.New("capacity root and content size are invalid")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(filepath.Clean(c.Root), &stat); err != nil {
		return 0, 0, err
	}
	return capacityDecision(saturatingProduct(uint64(stat.Bavail), uint64(stat.Bsize)), contentBytes)
}
