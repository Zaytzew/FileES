package localrepo

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrConversionWaiting is an expected pre-effect condition, such as an
	// autocommit still running. It must never hide a partially applied effect.
	ErrConversionWaiting   = errors.New("conversion is waiting for safe execution conditions")
	ErrConversionExecuting = errors.New("conversion already has an executor")
)

type ConversionPreparation struct {
	SourceFileID         string
	Revision             int64
	PlanPath, PlanSHA256 string
	LocalObjects         int64
}

type ConversionPoint struct{ FileID, ProviderID string }

// ConversionEffects is the mandatory trust boundary to the existing daemon
// provisioner/anchor manager. There are no permissive or os.RemoveAll defaults.
//
// Acquire owns the local-intent fence and quiesces supervision WITHOUT detach
// or metadata deletion. Waiting before preparation must let autocommit resume;
// later phases remain fenced by the durable lifecycle intent across restarts.
// Revalidate checks that exact attachment, reservations, authority and plan.
//
// Every effect must be idempotent after process death BEFORE its journal ACK:
// Retain proves the source FileID at exactly one path, no overwrite; Point
// adopts only this operation's durable point receipt, never arbitrary mkdir;
// Preserve uses per-object no-replace receipts; Verify confirms provider/tree
// and every local object; Cleanup rechecks late writes and has a durable
// retirement receipt (absence of the backup is NOT proof of cleanup).
// Rollback preserves new/ambiguous data, proves the restored source FileID and
// owns its own per-effect receipts. It must not delete an occupied new point.
// These contracts need concrete native adapters before enabling a GUI action.
type ConversionEffects interface {
	Acquire(context.Context, ConversionRecord) (release func(), err error)
	Revalidate(context.Context, ConversionRecord) error
	Prepare(context.Context, ConversionRecord) (ConversionPreparation, error)
	Retain(context.Context, ConversionRecord) error
	Point(context.Context, ConversionRecord) (ConversionPoint, error)
	Preserve(context.Context, ConversionRecord) error
	Verify(context.Context, ConversionRecord) error
	Cleanup(context.Context, ConversionRecord) error
	Rollback(context.Context, ConversionRecord) (restoredFileID string, err error)
}

// ConversionProgress reports actual effect/receipt boundaries, not percentages.
// An observer is advisory only; it cannot grant a receipt or suppress an error.
type ConversionProgress struct {
	Phase    ConversionPhase
	Boundary string
}

type ConversionExecutor struct {
	Journal  *ConversionJournal
	Effects  ConversionEffects
	Progress func(ConversionProgress)
}

// Run resumes the durable direction. A known blocked operation does not retry
// merely because the daemon restarted. The caller explicitly uses Journal.Retry.
func (e *ConversionExecutor) Run(ctx context.Context) (ConversionRecord, error) {
	return e.run(ctx, false)
}

// Rollback is an explicit user intent; the direction is persisted before the
// first recovery effect. Waiting conversions are canceled without touching WC.
func (e *ConversionExecutor) Rollback(ctx context.Context) (ConversionRecord, error) {
	return e.run(ctx, true)
}

func (e *ConversionExecutor) run(ctx context.Context, rollback bool) (ConversionRecord, error) {
	if e.Journal == nil || e.Effects == nil {
		return ConversionRecord{}, errors.New("conversion executor is not configured")
	}
	j := e.Journal
	if !j.runMu.TryLock() {
		return j.Get(), ErrConversionExecuting
	}
	defer j.runMu.Unlock()
	r := j.Get()
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if conversionTerminal(r.Phase) {
		if rollback && r.Phase == ConversionComplete {
			return r, errors.New("completed conversion cannot be rolled back by this executor")
		}
		return r, nil
	}
	if rollback {
		var err error
		if r.Phase == ConversionWaiting {
			return j.Cancel(r)
		}
		if r.Phase == ConversionRollingBack {
			if r.Blocked != "" {
				r, err = j.Retry(r)
			}
		} else {
			r, err = j.BeginRollback(r)
		}
		if err != nil {
			return j.Get(), err
		}
		e.report(r.Phase, "receipt")
	}
	if r.Blocked != "" {
		return r, fmt.Errorf("conversion blocked in %s: %s", r.Phase, r.Blocked)
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	release, err := e.Effects.Acquire(ctx, r)
	if err != nil {
		if release != nil {
			release()
		}
		return e.fail(ctx, r, err, true)
	}
	if release == nil {
		return e.fail(ctx, r, errors.New("conversion effects did not acquire an execution fence"), false)
	}
	defer release()
	if j.Get() != r {
		return j.Get(), errors.New("conversion receipt changed before execution")
	}
	for !conversionTerminal(r.Phase) {
		if err := ctx.Err(); err != nil {
			return j.Get(), err
		}
		if j.Get() != r {
			return j.Get(), errors.New("conversion receipt changed during execution")
		}
		if err := e.Effects.Revalidate(ctx, r); err != nil {
			return e.fail(ctx, r, err, true)
		}
		if err := ctx.Err(); err != nil {
			return j.Get(), err
		}
		var next ConversionRecord
		var stepErr error
		switch r.Phase {
		case ConversionWaiting:
			var p ConversionPreparation
			p, err = e.Effects.Prepare(ctx, r)
			if err != nil {
				return e.fail(ctx, r, err, true)
			}
			e.report(r.Phase, "effect")
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			proof := r
			proof.Phase, proof.Prepared, proof.SourceFileID = ConversionRetaining, true, p.SourceFileID
			proof.Revision, proof.PlanPath, proof.PlanSHA256, proof.LocalObjects = p.Revision, p.PlanPath, p.PlanSHA256, p.LocalObjects
			if err := validateConversion(proof); err != nil {
				return e.fail(ctx, r, err, false)
			}
			next, stepErr = j.Prepare(r, p.SourceFileID, p.Revision, p.PlanPath, p.PlanSHA256, p.LocalObjects)
		case ConversionRetaining:
			if err := e.Effects.Retain(ctx, r); err != nil {
				return e.fail(ctx, r, err, false)
			}
			e.report(r.Phase, "effect")
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			next, stepErr = j.MarkRetained(r)
		case ConversionRetained:
			// Intent before Point; no side effect is allowed in this transition.
			next, stepErr = j.BeginPoint(r)
		case ConversionCreating:
			var p ConversionPoint
			p, err = e.Effects.Point(ctx, r)
			if err != nil {
				return e.fail(ctx, r, err, false)
			}
			e.report(r.Phase, "effect")
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			proof := r
			proof.Phase, proof.PointFileID, proof.ProviderID = ConversionPreserving, p.FileID, p.ProviderID
			if err := validateConversion(proof); err != nil {
				return e.fail(ctx, r, err, false)
			}
			next, stepErr = j.MarkPointReady(r, p.FileID, p.ProviderID)
		case ConversionPreserving:
			if err := e.Effects.Preserve(ctx, r); err != nil {
				return e.fail(ctx, r, err, false)
			}
			e.report(r.Phase, "effect")
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			next, stepErr = j.MarkPreserved(r)
		case ConversionVerifying:
			if err := e.Effects.Verify(ctx, r); err != nil {
				return e.fail(ctx, r, err, false)
			}
			e.report(r.Phase, "effect")
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			next, stepErr = j.MarkVerified(r)
		case ConversionCleanup:
			// Verification is deliberately repeated after restart, immediately
			// before cleanup; an older successful Verify is not a current proof.
			if err := e.Effects.Verify(ctx, r); err != nil {
				return e.fail(ctx, r, err, false)
			}
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			if err := e.Effects.Cleanup(ctx, r); err != nil {
				return e.fail(ctx, r, err, false)
			}
			e.report(r.Phase, "effect")
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			next, stepErr = j.MarkComplete(r)
		case ConversionRollingBack:
			var id string
			id, err = e.Effects.Rollback(ctx, r)
			if err != nil {
				return e.fail(ctx, r, err, false)
			}
			e.report(r.Phase, "effect")
			if err := ctx.Err(); err != nil {
				return j.Get(), err
			}
			if id != r.SourceFileID {
				return e.fail(ctx, r, errors.New("rollback did not restore the original source identity"), false)
			}
			next, stepErr = j.MarkRolledBack(r, id)
		default:
			return e.fail(ctx, r, errors.New("conversion has an unsupported execution stage"), false)
		}
		// A write may have crossed rename/fsync before returning an error.
		// Stop and reload; do not write a second receipt using the old CAS.
		if stepErr != nil {
			return j.Get(), stepErr
		}
		r = next
		e.report(r.Phase, "receipt")
	}
	return r, nil
}

func (e *ConversionExecutor) report(phase ConversionPhase, boundary string) {
	if e.Progress != nil {
		e.Progress(ConversionProgress{Phase: phase, Boundary: boundary})
	}
}

func (e *ConversionExecutor) fail(ctx context.Context, r ConversionRecord, cause error, mayWait bool) (ConversionRecord, error) {
	if ctx.Err() != nil {
		return e.Journal.Get(), errors.Join(cause, ctx.Err())
	}
	if mayWait && r.Phase == ConversionWaiting && !r.Prepared && errors.Is(cause, ErrConversionWaiting) {
		return r, cause
	}
	if errors.Is(cause, ErrConversionWaiting) {
		cause = fmt.Errorf("conversion effect reported waiting after execution admission: %v", cause)
	}
	blocked, err := e.Journal.Block(r, cause)
	if err != nil {
		return e.Journal.Get(), errors.Join(cause, err)
	}
	return blocked, fmt.Errorf("conversion %s stopped in %s (source %s; retained copy %s): %w", r.ID, r.Phase, r.SourcePath, r.BackupPath, cause)
}
