package main

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

// A known refusal before any effect may be retried. A lost IPC answer,
// cancellation or partial batch must never be interpreted as that refusal.
func folderRefusal(err error, code, key string) bool {
	var refusal *ipcclient.ResponseError
	return errors.As(err, &refusal) && refusal.Body.Code == code && refusal.Body.MessageKey == key
}

func (s *HeadBrowserService) folderCurrent(repo HeadBrowserRepository, focus HeadBrowserContext, action string) error {
	current, err := s.focused()
	if err != nil {
		return &ipcclient.ResponseError{Body: contract.ErrorBody{Code: "HEAD-2001", MessageKey: "head.forbidden"}}
	}
	if s.Context() != focus || !current.CanBatch || current.LocalPath != repo.LocalPath || current.ServerID != repo.ServerID || current.RepoID != repo.RepoID || (action == "release" && !current.CanRelease) {
		return &ipcclient.ResponseError{Body: contract.ErrorBody{Code: "HEAD-2001", MessageKey: "head.forbidden"}}
	}
	return nil
}

func (s *HeadBrowserService) waitFolder(ctx context.Context, delay time.Duration) error {
	if s.folderWait != nil {
		return s.folderWait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func folderCallWhileBusy[T any](ctx context.Context, s *HeadBrowserService, guard func() error, waiting func() error, call func(context.Context) (*T, error)) (*T, error) {
	delay := 500 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := guard(); err != nil {
			return nil, err
		}
		value, err := call(ctx)
		if !folderRefusal(err, "HEAD-2018", "head.folder_busy") {
			return value, err
		}
		if waiting != nil {
			if err := waiting(); err != nil {
				return nil, err
			}
		}
		if err := s.waitFolder(ctx, delay); err != nil {
			return nil, err
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func (s *HeadBrowserService) folderFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New(s.render("HEAD-2021", "head.folder_wait_timeout", nil))
	}
	return s.failure(err, "HEAD-2006", "head.materialize_failed")
}

func (s *HeadBrowserService) folderPreview(ctx context.Context, d headBrowserBatchDaemon, repo HeadBrowserRepository, focus HeadBrowserContext, p contract.RepoAnchorPlanPayload, explainWait bool) (contract.RepoAnchorPlan, error) {
	var closeProgress func()
	defer func() {
		if closeProgress != nil {
			closeProgress()
		}
	}()
	waiting := func() error {
		if !explainWait || closeProgress != nil || s.showProgress == nil {
			return nil
		}
		var err error
		closeProgress, err = s.showProgress(ctx, platform.ProgressRequest{
			PresentationKey: "progress.anchorFolder.wait", PresentationArgs: map[string]string{"path": repo.Name},
			Title: "Czekam na zakończenie zapisu…", Text: "Folder kończy bieżący zapis. FileES poczeka i ponowi działanie.",
		})
		return err
	}
	plan, err := folderCallWhileBusy(ctx, s, func() error { return s.folderCurrent(repo, focus, p.Action) }, waiting, func(ctx context.Context) (*contract.RepoAnchorPlan, error) { return d.AnchorPlan(ctx, p) })
	if err != nil {
		return contract.RepoAnchorPlan{}, s.folderFailure(err)
	}
	if plan == nil {
		return contract.RepoAnchorPlan{}, s.folderFailure(errors.New("empty folder plan"))
	}
	return *plan, nil
}

func folderDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32
}

// Download is non-destructive and means the current bytes of these same
// paths. Refresh exact metadata only if the daemon proves the same attached
// subtree and no increase beyond the confirmed workload. Release never does
// this. Every execution still carries a fresh exact fingerprint.
func (s *HeadBrowserService) folderExecute(ctx context.Context, d headBrowserBatchDaemon, repo HeadBrowserRepository, focus HeadBrowserContext, p contract.RepoAnchorPlanPayload, approved contract.RepoAnchorPlan) (contract.RepoAnchorBatchResult, contract.RepoAnchorPlan, error) {
	if p.Action == "release" && s.releaseQueueOffered() {
		if queue, ok := s.daemon.(headBrowserReleaseQueueDaemon); ok {
			return s.queuedFolderRelease(ctx, queue, repo, focus, p, approved)
		}
	}
	var zero contract.RepoAnchorBatchResult
	plan := approved
	delay := 500 * time.Millisecond
	for {
		result, err := folderCallWhileBusy(ctx, s, func() error { return s.folderCurrent(repo, focus, p.Action) }, nil, func(ctx context.Context) (*contract.RepoAnchorBatchResult, error) {
			return d.AnchorBatch(ctx, contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: plan.Fingerprint})
		})
		if err == nil {
			if result == nil {
				return zero, plan, s.folderFailure(errors.New("empty folder result"))
			}
			return *result, plan, nil
		}
		if p.Action != "download" || !folderRefusal(err, "HEAD-2017", "head.folder_changed") || !folderDigest(approved.SelectionFingerprint) {
			return zero, plan, s.folderFailure(err)
		}
		if err := s.waitFolder(ctx, delay); err != nil {
			return zero, plan, s.folderFailure(err)
		}
		delay = min(delay*2, 5*time.Second)
		fresh, err := s.folderPreview(ctx, d, repo, focus, p, false)
		if err != nil {
			return zero, plan, err
		}
		if !fresh.Directory || !folderDigest(fresh.Fingerprint) || fresh.SelectionFingerprint != approved.SelectionFingerprint || fresh.Files < 0 || fresh.Bytes < 0 || fresh.Files > approved.Files || fresh.Bytes > approved.Bytes {
			return zero, plan, errors.New(s.render("HEAD-2017", "head.folder_changed", nil))
		}
		plan = fresh
		if plan.Files == 0 {
			return contract.RepoAnchorBatchResult{State: "attached", LocalPath: repo.LocalPath, Path: p.Path}, plan, nil
		}
	}
}
