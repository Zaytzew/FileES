//go:build windows

package repoworker

import (
	"context"
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type FilesystemCapacity struct{ Root string }

func (c FilesystemCapacity) Check(ctx context.Context, contentBytes int64) (int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if !filepath.IsAbs(c.Root) || contentBytes < 0 {
		return 0, 0, errors.New("capacity root and content size are invalid")
	}
	path, err := windows.UTF16PtrFromString(filepath.Clean(c.Root))
	if err != nil {
		return 0, 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, nil, nil); err != nil {
		return 0, 0, err
	}
	return capacityDecision(saturatingProduct(available, 1), contentBytes)
}
