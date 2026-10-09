package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/explorermenu"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
)

type explorerPlanDaemon struct {
	fakeHeadBatchDaemon
	notDirectory bool
	noWork       bool
}

func (d *explorerPlanDaemon) AnchorPlan(ctx context.Context, p contract.RepoAnchorPlanPayload) (*contract.RepoAnchorPlan, error) {
	plan, err := d.fakeHeadBatchDaemon.AnchorPlan(ctx, p)
	plan.Directory = !d.notDirectory
	if d.noWork {
		plan.Files, plan.Bytes = 0, 0
	}
	return plan, err
}

func TestExplorerVirtualFolderExplainsNoWorkWithoutMutation(t *testing.T) {
	for _, verb := range []string{explorermenu.Download, explorermenu.Release} {
		action := "download"
		if verb == explorermenu.Release {
			action = "release"
		}
		for _, child := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/child=%v", verb, child), func(t *testing.T) {
				snapshot := headBrowserSnapshot()
				snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorBatch, contract.CapRepoAnchorRelease)
				root := filepath.Join(t.TempDir(), "Point")
				snapshot.Repositories[1].LocalPath = root
				service, _ := newTestHeadBrowser(snapshot)
				d := &explorerPlanDaemon{noWork: true}
				service.daemon = d
				if err := service.Open("office", "remote"); err != nil {
					t.Fatal(err)
				}
				focus := service.Context()
				service.confirm = func(context.Context, platform.ConfirmRequest) (bool, error) {
					t.Fatal("no work asked for confirmation")
					return false, nil
				}
				service.showProgress = func(context.Context, platform.ProgressRequest) (func(), error) {
					t.Fatal("no work showed spinner")
					return nil, nil
				}
				completed := 0
				service.folderComplete = func(_ context.Context, gotAction string, done, total int64) {
					completed++
					if gotAction != action || done != 0 || total != 0 {
						t.Fatal(gotAction, done, total)
					}
				}
				selection := root
				if child {
					selection = filepath.Join(root, "Child")
				}
				if err := service.explorerFolder(explorermenu.Request{Verb: verb, Path: selection}); err != nil {
					t.Fatal(err)
				}
				if completed != 1 || len(d.batches) != 0 || service.Context() != focus {
					t.Fatal(completed, d.batches, service.Context())
				}
			})
		}
	}
}

func TestFolderCompletionInfoDistinguishesNoWorkFromTransfers(t *testing.T) {
	for _, action := range []string{"download", "release"} {
		for _, count := range []int64{0, 3} {
			got := folderCompletionInfo(action, count, count)
			key := "info.anchorFolder." + action
			if count == 0 {
				key += ".noWork"
			}
			if got.PresentationKey != key || got.PresentationArgs["total"] != fmt.Sprint(count) {
				t.Fatal(got)
			}
		}
	}
}

func TestExplorerVirtualFolderUsesConfirmedBatchWithoutChangingBrowser(t *testing.T) {
	for _, verb := range []string{explorermenu.Download, explorermenu.Release} {
		for _, variant := range []string{"root", "child", "cancel", "stale", "detach", "move", "capability", "ordinary", "outside", "ambiguous", "file", "no-prompt", "partial"} {
			t.Run(verb+"/"+variant, func(t *testing.T) {
				snapshot := headBrowserSnapshot()
				snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorBatch, contract.CapRepoAnchorRelease)
				root := filepath.Join(t.TempDir(), "Punkt żółty")
				snapshot.Repositories[1].LocalPath = root
				service, _ := newTestHeadBrowser(snapshot)
				service.projection = func() Snapshot { return snapshot }
				d := &explorerPlanDaemon{notDirectory: variant == "file"}
				d.partial = variant == "partial"
				service.daemon = d
				if err := service.Open("office", "remote"); err != nil {
					t.Fatal(err)
				}
				focus := service.Context()
				selection, relative := root, "."
				if variant == "child" {
					selection, relative = filepath.Join(root, "Zdjęcia", "Lato"), "Zdjęcia/Lato"
				}
				if variant == "outside" {
					selection = root + "-other"
				}
				if variant == "ordinary" {
					snapshot.Repositories[1].Sparse = false
				}
				if variant == "ambiguous" {
					snapshot.Repositories = append(snapshot.Repositories, snapshot.Repositories[1])
				}
				prompts := 0
				progress := newProgressService()
				shown, completed := 0, 0
				service.showProgress = func(ctx context.Context, p platform.ProgressRequest) (func(), error) {
					shown++
					if prompts != 1 || p.PresentationArgs["files"] != "3" {
						t.Fatal("spinner before confirmed plan", p)
					}
					return progress.ShowProgress(ctx, p)
				}
				service.folderComplete = func(_ context.Context, action string, done, total int64) {
					completed++
					if len(progress.Snapshot().Items) != 0 || done != 3 || total != 3 {
						t.Fatal("completion shown before closing spinner", done, total)
					}
				}
				service.confirm = func(_ context.Context, p platform.ConfirmRequest) (bool, error) {
					prompts++
					action := "download"
					if verb == explorermenu.Release {
						action = "release"
					}
					if p.PresentationKey != "confirm.anchorFolder."+action || p.PresentationArgs["path"] != relative {
						t.Fatal(p)
					}
					switch variant {
					case "stale":
						snapshot.Stale = true
					case "detach":
						snapshot.Repositories[1].Attached = false
					case "move":
						snapshot.Repositories[1].LocalPath = root + "-moved"
					case "capability":
						snapshot.Capabilities = []string{contract.CapRepoHeadBrowse}
					}
					return variant != "cancel", nil
				}
				if variant == "no-prompt" {
					service.confirm = nil
				}
				err := service.explorerFolder(explorermenu.Request{Verb: verb, Path: selection})
				switch variant {
				case "root", "child":
					if err != nil || prompts != 1 || len(d.batches) != 1 {
						t.Fatal(err, prompts, d.batches)
					}
					batch := d.batches[0]
					if batch.ServerID != "office" || batch.RepoID != "sparse" || batch.Path != relative || batch.Fingerprint != strings.Repeat("ab", 32) {
						t.Fatal(batch)
					}
				case "cancel":
					if err != nil || len(d.batches) != 0 {
						t.Fatal(err, d.batches)
					}
				case "partial":
					if err == nil || !strings.Contains(err.Error(), "1 z 3") || len(d.batches) != 1 {
						t.Fatal(err, d.batches)
					}
				default:
					if err == nil || len(d.batches) != 0 {
						t.Fatal("refusal performed mutation", err, d.batches)
					}
				}
				if service.Context() != focus {
					t.Fatal("Explorer changed visible browser focus")
				}
				if len(progress.Snapshot().Items) != 0 {
					t.Fatal("spinner left open")
				}
				wantShown, wantCompleted := 0, 0
				if variant == "root" || variant == "child" {
					wantShown, wantCompleted = 1, 1
				}
				if variant == "partial" {
					wantShown = 1
				}
				if shown != wantShown || completed != wantCompleted {
					t.Fatal("incorrect wait/result", shown, completed)
				}
				if len(d.materialized) != 0 || len(d.fills) != 0 || len(d.releases) != 0 || len(d.anchors) != 0 {
					t.Fatal("alternate SVN/lifecycle mutation")
				}
			})
		}
	}
}
