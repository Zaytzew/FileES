package servertool

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"filees/pkg/activation"
	"filees/pkg/repoworker"
)

func withServiceWorkingCopy(ctx context.Context, config activation.Config, fn func() error) error {
	return repoworker.WithFileLock(filepath.Join(config.Root, ".service-wc.lock"), func() error {
		if err := repoworker.ReconcileServiceWorkingCopy(ctx, config.SVNBinary, config.ServiceWorkingCopy); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return errors.Join(err, repoworker.ReconcileServiceWorkingCopy(cleanupCtx, config.SVNBinary, config.ServiceWorkingCopy))
	})
}
