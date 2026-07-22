//go:build windows

package repoworker

import (
	"context"
	"errors"
)

type FilesystemCapacity struct{ Root string }

func (FilesystemCapacity) Check(context.Context, int64) (int64, int64, error) {
	return 0, 0, errors.New("repository server capacity checks are unsupported on Windows")
}
