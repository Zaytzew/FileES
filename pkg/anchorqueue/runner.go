package anchorqueue

import (
	"context"
	"errors"
	"sync"

	contract "filees/pkg/contract/v1"
)

// Runner uses the daemon's existing tick. It owns no timer or resident worker.
// Only a typed pre-effect busy error can return running to queued.
type Runner struct{ mu sync.Mutex }

func (r *Runner) Tick(ctx context.Context, store *Store, busy error,
	ready func(Intent) (bool, error), execute func(context.Context, Intent) (contract.RepoAnchorBatchResult, error)) error {
	if !r.mu.TryLock() {
		return nil
	}
	defer r.mu.Unlock()
	for _, x := range store.List() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if x.Result.State != "queued" {
			continue
		}
		ok, err := ready(x)
		if err != nil {
			if errors.Is(err, busy) {
				continue
			}
			return err // readiness failures must be projected by the adapter, not guessed here
		}
		if !ok {
			continue
		}
		if err := store.Set(x.Payload.IntentID, "queued", contract.RepoAnchorBatchResult{State: "running"}); err != nil {
			return err
		}
		result, err := execute(ctx, x)
		if errors.Is(err, busy) {
			return store.Set(x.Payload.IntentID, "running", contract.RepoAnchorBatchResult{State: "queued"})
		}
		if err != nil {
			failure := contract.RepoAnchorBatchResult{State: "failed", FailureCode: "HEAD-2025", FailureKey: "head.release_queue_interrupted", FailureDetail: Diagnostic(err)}
			return errors.Join(err, store.Set(x.Payload.IntentID, "running", failure))
		} // adapter returns terminal typed failure; running is never retried on an unknown error
		if result.State == "partial" {
			result.State = "failed"
		}
		return store.Set(x.Payload.IntentID, "running", result)
	}
	return nil
}
