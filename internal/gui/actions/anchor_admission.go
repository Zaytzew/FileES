package actions

import (
	"context"
	"errors"
	"filees/pkg/errcat"
	"time"
)

// Retry only the daemon's dedicated, pre-effect admission refusal. Diagnostics,
// lost ACKs, timeouts and partially begun operations never authorize a replay.
func (c *Controller) createAnchorWhenReady(ctx context.Context, creator RepositoryAnchorCreator, serverID, repoID, localPath string) (string, error) {
	timeout, interval := 2*time.Minute, 500*time.Millisecond
	if d := c.cfg.CreationStatusPollTimeout; d > 0 && d < timeout {
		timeout = d
	}
	if d := c.cfg.CreationStatusPollInterval; d > 0 {
		interval = d
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !time.Now().Before(deadline) {
			return "", errcat.New("head.anchor_wait_timeout", nil, nil)
		}
		vm := c.cfg.ViewModel()
		if _, ok := attachableRepository(vm, serverID, repoID); !ok || !vm.CanExplorerAnchor() {
			return "", errcat.New("head.folder_unavailable", nil, nil)
		}
		remaining := time.Until(deadline)
		if remaining > 10*time.Second {
			remaining = 10 * time.Second
		}
		callCtx, cancel := context.WithTimeout(ctx, remaining)
		operationID, err := creator.CreateAnchor(callCtx, serverID, repoID, localPath)
		cancel()
		if err == nil {
			return operationID, nil
		}
		var presented presentationError
		if operationID != "" || !errors.As(err, &presented) {
			return "", err
		}
		code, _, _, key := presented.PresentationError()
		if code != "HEAD-2026" || key != "head.anchor_busy" {
			return "", err
		}
		delay := interval
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			return "", errcat.New("head.anchor_wait_timeout", nil, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}
