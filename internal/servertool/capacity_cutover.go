package servertool

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"filees/internal/storagewatch"
	"filees/pkg/activation"
	"filees/pkg/serverconfig"
	"filees/public-shares/intake"
)

// The monitor serializes samples with capacity.lock. Admission is stopped
// before waiting for writers; the exclusive lease precedes the WC lock.
// A failed drain remains pending and is never reported as an acknowledged RO.
func applyCapacityCutover(ctx context.Context, c serverconfig.Config, state storagewatch.State, statePath string) error {
	gate := storagewatch.Gate{Root: c.Activation.Root}
	phase, err := gate.Phase()
	if err != nil {
		return err
	}
	now := time.Now()
	if state.Observation == nil || state.Observation.MeasuredAt.After(now) || !state.Observation.MeasuredAt.After(now.Add(-storagewatch.ObservationTTL)) {
		return errors.New("capacity cutover requires a fresh measurement")
	}
	if !state.WritesHeld() && phase == "open" {
		return state.Save(statePath)
	}
	// Persist the device identity/hysteresis before installing the global stop.
	// A failed save cannot erase an earlier stop; no policy is resumed here.
	if state.WritesHeld() {
		if err := state.Save(statePath); err != nil {
			return err
		}
		if phase == "open" {
			if err := gate.Hold(); err != nil {
				return err
			}
			phase = "draining"
		}
	}
	manager, err := activation.NewUnderServiceWorkingCopyLock(c.Activation, nil)
	if err != nil {
		return err
	}
	if phase == "draining" {
		// Whale children also hold a shared writer lease. Signal them before
		// waiting on the exclusive gate, then require the real acknowledgement.
		if err := manager.SignalAllSessions(ctx); err != nil {
			return err
		}
	}
	policy := storagewatch.RepositoryPolicy{Root: c.Repositories.Root, JournalRoot: filepath.Join(c.Activation.Root, "capacity-repositories")}
	return gate.WithExclusive(ctx, func() error {
		cutover := func() error {
			return withServiceWorkingCopy(ctx, c.Activation, func() error {
				publish := func() error {
					if state.WritesHeld() {
						if err := policy.Hold(); err != nil {
							return err
						}
						return gate.AcknowledgeHeld()
					}
					if err := policy.Restore(); err != nil {
						return err
					}
					if err := state.Save(statePath); err != nil {
						return err
					}
					// The exclusive wait may consume the whole observation lifetime.
					if !state.Observation.MeasuredAt.After(time.Now().Add(-storagewatch.ObservationTTL)) {
						return errors.New("capacity recovery measurement expired")
					}
					return gate.Resume() // last durable step, never a timeout fallback
				}
				if phase == "draining" {
					return manager.WithAllSessionsStopped(ctx, publish)
				}
				// Held sessions were admitted with svnserve -R. Do not terminate
				// those readers on every cron tick, or on ordinary recovery.
				return publish()
			})
		}
		if c.Upload.Enabled() {
			return intake.WithReceiveBarrier(ctx, c.Upload.IntakeRoot, true, cutover)
		}
		return cutover()
	})
}
