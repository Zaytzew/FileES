package main

import (
	"context"
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
}

func (d *explorerPlanDaemon) AnchorPlan(ctx context.Context, p contract.RepoAnchorPlanPayload) (*contract.RepoAnchorPlan, error) {
	plan, err := d.fakeHeadBatchDaemon.AnchorPlan(ctx, p)
	plan.Directory = !d.notDirectory
	return plan, err
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
				if len(d.materialized) != 0 || len(d.fills) != 0 || len(d.releases) != 0 || len(d.anchors) != 0 {
					t.Fatal("alternate SVN/lifecycle mutation")
				}
			})
		}
	}
}
