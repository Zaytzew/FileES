package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

type queueBrowserDaemon struct {
	fakeHeadBatchDaemon
	submitted []contract.RepoAnchorQueuePayload
	reads     int
	variant   string
	root      string
}

func (d *queueBrowserDaemon) result() *contract.RepoAnchorQueueResult {
	p := d.submitted[0]
	r := &contract.RepoAnchorQueueResult{IntentID: p.IntentID, ServerID: p.ServerID, RepoID: p.RepoID,
		RepoAnchorBatchResult: contract.RepoAnchorBatchResult{State: "queued", Path: p.Path, LocalPath: d.root, Total: p.Files}}
	if d.reads > 1 {
		r.State = "attached"
		r.Completed = r.Total
	}
	if d.variant == "failed" {
		r.State = "failed"
		r.FailureCode = "HEAD-2017"
		r.FailureKey = "head.folder_changed"
	}
	return r
}
func (d *queueBrowserDaemon) AnchorReleaseQueue(_ context.Context, p contract.RepoAnchorQueuePayload) (*contract.RepoAnchorQueueResult, error) {
	d.submitted = append(d.submitted, p)
	if d.variant == "refused-preview" {
		return nil, &ipcclient.ResponseError{Body: contract.ErrorBody{Code: "HEAD-2017", MessageKey: "head.folder_changed"}}
	}
	if d.variant == "lost-submit" {
		return nil, errors.New("transport lost")
	}
	return d.result(), nil
}
func (d *queueBrowserDaemon) AnchorReleaseQueueStatus(_ context.Context, p contract.RepoAnchorQueueStatusPayload) (*contract.RepoAnchorQueueResult, error) {
	d.reads++
	if p.IntentID != d.submitted[0].IntentID {
		panic("status queried a different confirmation")
	}
	if d.variant == "lost-status" {
		return nil, errors.New("transport lost")
	}
	return d.result(), nil
}

func TestHeadBrowserDurableReleaseQueuesOnceAndOnlyPolls(t *testing.T) {
	for _, v := range []string{"complete", "lost-submit", "lost-status", "failed", "timeout", "cancelled", "refused-preview"} {
		t.Run(v, func(t *testing.T) {
			snapshot := headBrowserSnapshot()
			snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorRelease, contract.CapRepoAnchorBatch, contract.CapRepoAnchorReleaseQueue)
			s, _ := newTestHeadBrowser(snapshot)
			d := &queueBrowserDaemon{variant: v}
			s.daemon = d
			if err := s.Open("office", "sparse"); err != nil {
				t.Fatal(err)
			}
			repo, err := s.focused()
			if err != nil {
				t.Fatal(err)
			}
			d.root = repo.LocalPath
			confirms, progress := 0, false
			s.confirm = func(context.Context, platform.ConfirmRequest) (bool, error) { confirms++; return v != "cancelled", nil }
			s.showProgress = func(context.Context, platform.ProgressRequest) (func(), error) {
				progress = true
				return func() { progress = false }, nil
			}
			s.folderWait = func(context.Context, time.Duration) error {
				if !progress {
					t.Fatal("no spinner")
				}
				if v == "timeout" {
					return context.DeadlineExceeded
				}
				return nil
			}
			kind := ""
			if v == "timeout" {
				s.folderTimeout = time.Millisecond
				s.folderWait = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
			}
			s.folderComplete = func(ctx context.Context, k string, _, _ int64) {
				if ctx.Err() != nil {
					t.Fatal("information window inherited expired wait", ctx.Err())
				}
				kind = k
			}
			result, handled, err := s.folderAction(repo, ".", "release")
			if !handled || confirms != 1 || progress || len(d.batches) != 0 {
				t.Fatal(result, handled, err, confirms, progress, d.batches)
			}
			if v == "cancelled" {
				if len(d.submitted) != 0 || err != nil {
					t.Fatal(d.submitted, err)
				}
				return
			}
			if len(d.submitted) != 1 {
				t.Fatal("confirmation submitted more than once", d.submitted)
			}
			switch v {
			case "failed", "refused-preview":
				if err == nil || d.reads != 0 {
					t.Fatal(err, d.reads)
				}
			case "lost-status":
				if err != nil || result.State != "queue_unknown" || kind != "releaseUnknown" {
					t.Fatal(result, err, kind)
				}
			case "timeout":
				if err != nil || result.State != "queued" || kind != "releaseQueued" {
					t.Fatal(result, err, kind)
				}
			default:
				if err != nil || result.State != "attached" || kind != "release" {
					t.Fatal(result, err, kind)
				}
			}
		})
	}
}
