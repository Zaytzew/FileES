package commit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
)

const workingCopyRecoveryName = "working-copy-recovery.json"

var ErrWorkingCopyRecoveryPending = errors.New("unfinished local SVN operation is awaiting recovery")

func IsWorkingCopyRecoveryWaiting(err error) bool {
	return errors.Is(err, ErrWorkingCopyRecoveryPending) || errors.Is(err, ErrLocalStatusBusy)
}

func workingCopyRecoveryPath(wc string) string {
	return filepath.Join(wc, ".filees", "state", workingCopyRecoveryName)
}

// This marker is only a conservative writer fence, never deletion authority.
// Unknown/unreadable metadata also holds the fence. SVN owns its work queue.
func HasPendingWorkingCopyRecovery(wc string) bool {
	_, err := os.Lstat(workingCopyRecoveryPath(wc))
	return !errors.Is(err, os.ErrNotExist)
}

func (s *Service) setWorkingCopyRecovery(wc, phase string, cause error) error {
	s.workingRecoveryRequired.Store(true)
	status := &contract.WorkingCopyRecoveryStatus{Phase: phase}
	if cause != nil {
		status.Detail = cause.Error()
	}
	marker := struct {
		Schema string `json:"schema"`
		Root   string `json:"root"`
		*contract.WorkingCopyRecoveryStatus
	}{"filees.working-copy-recovery/v1", wc, status}
	raw, err := json.Marshal(marker)
	if err == nil {
		err = atomicWriteStringInExistingDir(workingCopyRecoveryPath(wc), string(raw))
	}
	if err != nil {
		status.Phase, status.Detail = "blocked", "persist local recovery: "+err.Error()
	}
	if s.OnWorkingCopyRecovery != nil {
		s.OnWorkingCopyRecovery(status)
	}
	return err
}

// RecoverWorkingCopy is startup-only before the service's periodic workers.
// It does not call another locking service method from a native callback.
func (s *Service) RecoverWorkingCopy(ctx context.Context, wc string) (string, error) {
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	return s.recoverWorkingCopy(ctx, wc)
}

func (s *Service) recoverWorkingCopy(ctx context.Context, wc string) (out string, resultErr error) {
	if HasUnresolvedCommit(wc) || HasPendingLocalTransaction(wc) {
		return "", ErrWorkingCopyRecoveryPending
	}
	if err := s.setWorkingCopyRecovery(wc, "running", nil); err != nil {
		return "", err
	}
	queueDrained := false
	defer func() {
		if resultErr == nil || queueDrained {
			return
		}
		phase := "blocked"
		if ctx.Err() != nil || IsWorkingCopyRecoveryWaiting(resultErr) || client.IsNetworkError(resultErr) {
			phase = "waiting"
		}
		resultErr = errors.Join(resultErr, s.setWorkingCopyRecovery(wc, phase, resultErr))
	}()
	if _, err := s.Cli.Cleanup(ctx, wc); err != nil {
		return "", err
	}
	status, err := s.Cli.Status(ctx, wc, nil)
	if err != nil {
		return "", err
	}
	if BlocksUpdate(wc, status) {
		// Cleanup has positively drained the SVN work queue. These are the
		// user's local removals waiting to publish, not unfinished SVN work.
		// Retaining our fence here would prevent the very commit we await.
		if err := s.completeWorkingCopyProjection(ctx, wc); err != nil {
			return "", err
		}
		if err := s.finishWorkingCopyRecovery(wc); err != nil {
			return "", err
		}
		queueDrained = true
		return "", ErrWorkingCopyRecoveryPending
	}
	out, err = s.Cli.Update(ctx, wc)
	if err != nil {
		return out, err
	}
	s.ReconcileUpdateConflicts(ctx, wc, out)
	if err := s.completeWorkingCopyProjection(ctx, wc); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := s.finishWorkingCopyRecovery(wc); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) completeWorkingCopyProjection(ctx context.Context, wc string) error {
	if projection, ok := s.Cli.(client.LocalProjectionCompleter); ok {
		return projection.CompleteWorkingCopyProjection(ctx, wc)
	}
	return nil
}

func (s *Service) finishWorkingCopyRecovery(wc string) error {
	if err := os.Remove(workingCopyRecoveryPath(wc)); err != nil {
		return err
	}
	s.workingRecoveryRequired.Store(false)
	if s.OnWorkingCopyRecovery != nil {
		s.OnWorkingCopyRecovery(nil)
	}
	return nil
}
