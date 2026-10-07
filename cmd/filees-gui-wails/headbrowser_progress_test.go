package main

import (
	"context"
	"errors"
	"testing"

	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
)

type inspectingBatchDaemon struct {
	fakeHeadBatchDaemon
	inspect func()
	err     error
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
