package servertool

import (
	"context"
	"errors"
	"path/filepath"

	"filees/internal/storagewatch"
	"filees/pkg/activation"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"filees/public-shares/intake"
)

// withInstallationWritersAndSessionsStopped is an INTERNAL first step, not an
// installer entry point. Caller must validate both signed manifests separately.
// Its callback may inspect/prepare receipts, but must not replace executables:
// resident-service lifecycle and compatible rollback receipts are not yet wired.
// No durable "drained" flag, PID kill, policy resume or archive bypass exists.
// Call outside WithWriter/service-WC locks: it acquires writer -> worker ->
// receive -> service-WC -> activation barriers in that order.
func withInstallationWritersAndSessionsStopped(ctx context.Context, c serverconfig.Config, binding storagewatch.InstallationBinding, inspect func() error) error {
	if inspect == nil {
		return errors.New("installation inspection is required")
	}
	// Existing local authority: do not let a remote repo owner or root over a
	// foreign service WC persist a stop or signal unrelated sessions.
	if err := repoworker.CheckServiceWorkingCopyOwner(c.Activation.ServiceWorkingCopy); err != nil {
		return err
	}
	gate := storagewatch.Gate{Root: c.Activation.Root}
	if err := gate.RequestInstallationStop(ctx, binding); err != nil {
		return err
	}
	manager, err := activation.New(c.Activation, nil)
	if err != nil {
		return err
	}
	// Whale can hold the writer lease itself. Signal before waiting, then
	// require actual session ACK under the normal activation lock below.
	if err := manager.SignalAllSessions(ctx); err != nil {
		return err
	}
	return gate.WithInstallationWritersStopped(ctx, binding, func() error {
		return repoworker.WithFileLockContext(ctx, filepath.Join(c.Repositories.ResultsRoot, ".worker.lock"), func() error {
			drain := func() error {
				return withServiceWorkingCopy(ctx, c.Activation, func() error {
					manager, err := activation.NewUnderServiceWorkingCopyLock(c.Activation, nil)
					if err != nil {
						return err
					}
					return manager.WithAllSessionsStopped(ctx, inspect)
				})
			}
			if c.Upload.Enabled() {
				return intake.WithReceiveBarrier(ctx, c.Upload.IntakeRoot, true, drain)
			}
			return drain()
		})
	})
}
