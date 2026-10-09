package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

type fakeHeadBatchDaemon struct {
	fakeHeadBrowserDaemon
	plans   []contract.RepoAnchorPlanPayload
	batches []contract.RepoAnchorBatchPayload
	partial bool
}

type missingPathBatchDaemon struct{ fakeHeadBatchDaemon }

func (d *missingPathBatchDaemon) AnchorPlan(_ context.Context, p contract.RepoAnchorPlanPayload) (*contract.RepoAnchorPlan, error) {
	d.plans = append(d.plans, p)
	return nil, &ipcclient.ResponseError{Body: contract.ErrorBody{Code: "HEAD-2023", MessageKey: "head.folder_path_missing", Hint: "REQUIRE_ACTION"}}
}

func TestHeadBrowserMissingPathNeverRetriesOrMutates(t *testing.T) {
	for _, action := range []string{"download", "release"} {
		t.Run(action, func(t *testing.T) {
			snapshot := headBrowserSnapshot()
			snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorBatch, contract.CapRepoAnchorRelease)
			service, _ := newTestHeadBrowser(snapshot)
			d := &missingPathBatchDaemon{}
			service.daemon = d
			if err := service.Open("office", "sparse"); err != nil {
				t.Fatal(err)
			}
			service.folderWait = func(context.Context, time.Duration) error { t.Fatal("missing path retried"); return nil }
			service.confirm = func(context.Context, platform.ConfirmRequest) (bool, error) {
				t.Fatal("missing path confirmed")
				return false, nil
			}
			service.showProgress = func(context.Context, platform.ProgressRequest) (func(), error) {
				t.Fatal("missing path waited as busy")
				return nil, nil
			}
			repo, err := service.focused()
			if err != nil {
				t.Fatal(err)
			}
			_, handled, err := service.folderAction(repo, ".", action)
			if !handled || err == nil || len(d.plans) != 1 || len(d.batches) != 0 {
				t.Fatal(handled, err, d.plans, d.batches)
			}
		})
	}
}

func (f *fakeHeadBatchDaemon) AnchorPlan(_ context.Context, p contract.RepoAnchorPlanPayload) (*contract.RepoAnchorPlan, error) {
	f.plans = append(f.plans, p)
	return &contract.RepoAnchorPlan{Directory: true, Files: 3, Bytes: 30, Fingerprint: strings.Repeat("ab", 32)}, nil
}
func (f *fakeHeadBatchDaemon) AnchorBatch(_ context.Context, p contract.RepoAnchorBatchPayload) (*contract.RepoAnchorBatchResult, error) {
	f.batches = append(f.batches, p)
	result := &contract.RepoAnchorBatchResult{Path: p.Path, State: "attached", Completed: 3, Total: 3}
	if f.partial {
		result.State = "partial"
		result.Completed = 1
	}
	return result, nil
}

func TestHeadBrowserFolderRequiresConsentAndKeepsFocusedAttachment(t *testing.T) {
	for _, variant := range []string{"accepted", "cancelled", "focus", "attachment", "capability", "partial", "missing-prompt"} {
		t.Run(variant, func(t *testing.T) {
			snapshot := headBrowserSnapshot()
			snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorBatch, contract.CapRepoAnchorRelease)
			service, _ := newTestHeadBrowser(snapshot)
			service.projection = func() Snapshot { return snapshot }
			daemon := &fakeHeadBatchDaemon{partial: variant == "partial"}
			service.daemon = daemon
			if err := service.Open("office", "sparse"); err != nil {
				t.Fatal(err)
			}
			service.confirm = func(_ context.Context, r platform.ConfirmRequest) (bool, error) {
				if r.PresentationKey != "confirm.anchorFolder.download" || r.PresentationArgs["files"] != "3" || r.PresentationArgs["bytes"] != "30" {
					t.Fatal(r)
				}
				switch variant {
				case "focus":
					_ = service.Open("office", "remote")
				case "attachment":
					snapshot.Repositories[1].LocalPath += "-other"
				case "capability":
					snapshot.Capabilities = []string{contract.CapRepoHeadBrowse}
				}
				return variant != "cancelled", nil
			}
			if variant == "missing-prompt" {
				service.confirm = nil
			}
			result, err := service.Materialize("dir")
			switch variant {
			case "accepted":
				if err != nil || result.State != "attached" || len(daemon.batches) != 1 {
					t.Fatal(result, err)
				}
				if daemon.batches[0].Fingerprint != strings.Repeat("ab", 32) {
					t.Fatal("confirmation lost")
				}
			case "partial":
				if err == nil || !strings.Contains(err.Error(), "1 z 3") || len(daemon.batches) != 1 {
					t.Fatal(result, err)
				}
			case "cancelled":
				if err != nil || result.State != "cancelled" || len(daemon.batches) != 0 {
					t.Fatal(result, err)
				}
			default:
				if err == nil || len(daemon.batches) != 0 {
					t.Fatal("stale selection written", result, err)
				}
			}
			if len(daemon.materialized) != 0 || len(daemon.fills) != 0 {
				t.Fatal("ordinary sparse fallback")
			}
		})
	}
}
