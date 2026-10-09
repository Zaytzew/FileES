package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

type inspectingBatchDaemon struct {
	fakeHeadBatchDaemon
	inspect func()
	err     error
}

type waitingFolderDaemon struct {
	fakeHeadBatchDaemon
	plansCalled, batchesCalled int
	preview                    func(int) (*contract.RepoAnchorPlan, error)
	execute                    func(int) (*contract.RepoAnchorBatchResult, error)
}

func (d *waitingFolderDaemon) AnchorPlan(_ context.Context, _ contract.RepoAnchorPlanPayload) (*contract.RepoAnchorPlan, error) {
	d.plansCalled++
	return d.preview(d.plansCalled)
}
func (d *waitingFolderDaemon) AnchorBatch(_ context.Context, _ contract.RepoAnchorBatchPayload) (*contract.RepoAnchorBatchResult, error) {
	d.batchesCalled++
	return d.execute(d.batchesCalled)
}
func folderTestRefusal(code, key string) error {
	return &ipcclient.ResponseError{Body: contract.ErrorBody{Code: code, MessageKey: key}}
}

func TestFolderWaitRetriesOnlyKnownPreEffectRefusals(t *testing.T) {
	for _, variant := range []string{"preview-busy", "busy-and-revision", "selection-changed", "workload-grew", "invalid-digest", "release-changed", "legacy-changed", "timeout", "cancel", "lost-answer", "pending-release", "recovery-hold", "partial", "first-file-unconfirmed", "moved", "revoked", "focus"} {
		t.Run(variant, func(t *testing.T) {
			snapshot := headBrowserSnapshot()
			snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorBatch, contract.CapRepoAnchorRelease)
			service, _ := newTestHeadBrowser(snapshot)
			service.projection = func() Snapshot { return snapshot }
			progress := newProgressService()
			service.showProgress = progress.ShowProgress
			confirmations, waits := 0, 0
			service.confirm = func(context.Context, platform.ConfirmRequest) (bool, error) { confirmations++; return true, nil }
			service.folderWait = func(_ context.Context, delay time.Duration) error {
				waits++
				if delay <= 0 || delay > 5*time.Second || len(progress.Snapshot().Items) != 1 {
					t.Fatal("wait lacks bounded honest spinner", delay, progress.Snapshot())
				}
				switch variant {
				case "timeout":
					return context.DeadlineExceeded
				case "cancel":
					return context.Canceled
				case "moved":
					snapshot.Repositories[1].LocalPath += "-moved"
				case "revoked":
					snapshot.Repositories[1].Access = ""
				case "focus":
					_ = service.Open("office", "remote")
				}
				return nil
			}
			d := &waitingFolderDaemon{}
			d.preview = func(n int) (*contract.RepoAnchorPlan, error) {
				if variant == "preview-busy" && n <= 2 {
					return nil, folderTestRefusal("HEAD-2018", "head.folder_busy")
				}
				consent := strings.Repeat("ab", 32)
				if variant == "selection-changed" && n > 1 {
					consent = strings.Repeat("ef", 32)
				}
				if variant == "legacy-changed" {
					consent = ""
				}
				plan := &contract.RepoAnchorPlan{Directory: true, Files: 3, Bytes: 30, Fingerprint: strings.Repeat("cd", 32), SelectionFingerprint: consent}
				if n > 1 && variant == "workload-grew" {
					plan.Files++
					plan.Bytes++
				}
				if n > 1 && variant == "invalid-digest" {
					plan.Fingerprint = "not-a-digest"
				}
				return plan, nil
			}
			d.execute = func(n int) (*contract.RepoAnchorBatchResult, error) {
				if len(progress.Snapshot().Items) != 1 {
					t.Fatal("execution escaped spinner")
				}
				if variant == "busy-and-revision" && n <= 2 {
					return nil, folderTestRefusal("HEAD-2018", "head.folder_busy")
				}
				if (variant == "busy-and-revision" && n == 3) || variant == "selection-changed" || variant == "workload-grew" || variant == "invalid-digest" || variant == "release-changed" || variant == "legacy-changed" {
					return nil, folderTestRefusal("HEAD-2017", "head.folder_changed")
				}
				switch variant {
				case "timeout", "cancel", "moved", "revoked", "focus":
					return nil, folderTestRefusal("HEAD-2018", "head.folder_busy")
				case "lost-answer":
					return nil, io.EOF
				case "pending-release":
					return nil, folderTestRefusal("HEAD-2016", "head.release_pending")
				case "recovery-hold":
					return nil, folderTestRefusal("HEAD-2019", "head.folder_recovery_required")
				case "partial":
					return &contract.RepoAnchorBatchResult{State: "partial", Completed: 1, Total: 3, FailureCode: "HEAD-2022", FailureKey: "head.folder_permission"}, nil
				case "first-file-unconfirmed":
					return &contract.RepoAnchorBatchResult{State: "partial", Completed: 0, Total: 3, FailureCode: "HEAD-2018", FailureKey: "head.folder_busy"}, nil
				}
				return &contract.RepoAnchorBatchResult{State: "attached", Completed: 3, Total: 3}, nil
			}
			service.daemon = d
			if err := service.Open("office", "sparse"); err != nil {
				t.Fatal(err)
			}
			repo, _ := service.focused()
			action := "download"
			if variant == "release-changed" {
				action = "release"
			}
			_, handled, err := service.folderAction(repo, "dir", action)
			success := variant == "preview-busy" || variant == "busy-and-revision"
			if !handled || (err == nil) != success {
				t.Fatal("unexpected outcome", handled, err)
			}
			if len(progress.Snapshot().Items) != 0 {
				t.Fatal("spinner leaked")
			}
			if confirmations != 1 {
				t.Fatal("confirmation repeated", confirmations)
			}
			if variant == "preview-busy" && (waits != 2 || d.plansCalled != 3 || d.batchesCalled != 1) {
				t.Fatal(waits, d.plansCalled, d.batchesCalled)
			}
			if variant == "busy-and-revision" && (waits != 3 || d.plansCalled != 2 || d.batchesCalled != 4) {
				t.Fatal(waits, d.plansCalled, d.batchesCalled)
			}
			if !success && d.batchesCalled != 1 {
				t.Fatal("unsafe mutation retry", d.batchesCalled)
			}
			if variant == "partial" && !strings.Contains(err.Error(), "head.folder_permission") {
				t.Fatal("lost partial refusal reason", err)
			}
		})
	}
}

func (d *inspectingBatchDaemon) AnchorBatch(ctx context.Context, p contract.RepoAnchorBatchPayload) (*contract.RepoAnchorBatchResult, error) {
	d.inspect()
	if d.err != nil {
		return nil, d.err
	}
	return d.fakeHeadBatchDaemon.AnchorBatch(ctx, p)
}

func TestFolderSpinnerCoversBatchAndClosesOnEveryResult(t *testing.T) {
	for _, action := range []string{"download", "release"} {
		for _, outcome := range []string{"success", "partial", "error", "cancelled-context"} {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				snapshot := headBrowserSnapshot()
				snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorBatch, contract.CapRepoAnchorRelease)
				service, _ := newTestHeadBrowser(snapshot)
				progress := newProgressService()
				service.showProgress = progress.ShowProgress
				service.confirm = func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil }
				d := &inspectingBatchDaemon{}
				d.partial = outcome == "partial"
				if outcome == "error" {
					d.err = errors.New("test refusal")
				}
				if outcome == "cancelled-context" {
					d.err = context.Canceled
				}
				d.inspect = func() {
					items := progress.Snapshot().Items
					if len(items) != 1 || items[0].PresentationKey != "progress.anchorFolder."+action || items[0].Measure != nil {
						t.Fatal("batch not covered by an honest spinner", items)
					}
				}
				service.daemon = d
				if err := service.Open("office", "sparse"); err != nil {
					t.Fatal(err)
				}
				repo, err := service.focused()
				if err != nil {
					t.Fatal(err)
				}
				_, handled, err := service.folderAction(repo, "dir", action)
				if !handled || (err == nil) != (outcome == "success") {
					t.Fatal(handled, err)
				}
				if len(progress.Snapshot().Items) != 0 {
					t.Fatal("spinner survived end")
				}
			})
		}
	}
}
