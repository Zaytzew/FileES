//go:build openbsd

package repoworker

import (
	"context"
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

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
	available := int64(0)
	if stat.F_bavail > 0 {
		available = saturatingProduct(uint64(stat.F_bavail), uint64(stat.F_bsize))
	}
	return capacityDecision(available, contentBytes)
}
