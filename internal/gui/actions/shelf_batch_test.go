package actions

import (
	"context"
	"errors"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"fmt"
	"testing"
)

type batchShelfProbe struct {
	parent     string
	ids        []string
	listed     int
	deny       bool
	failSecond bool
	cancel     context.CancelFunc
}

func (p *batchShelfProbe) ListShelf(context.Context, string, string) (ShelfList, error) {
	p.listed++
	return ShelfList{ChannelID: "channel", Items: []ShelfItem{{UploadID: "one"}, {UploadID: "two"}, {UploadID: "three"}}}, nil
}
func (p *batchShelfProbe) ShelfFetch(_ context.Context, _, _, _, _, _ string, inspect bool) (ShelfDownload, error) {
	if !inspect {
		return ShelfDownload{}, errors.New("unexpected standalone fetch")
	}
	return ShelfDownload{CanImport: !p.deny, ParentPath: p.parent}, nil
}
func (p *batchShelfProbe) ShelfImport(_ context.Context, server, repo, channel, upload, cache, destination string) (ShelfDownload, error) {
	if server != "server" || repo != "repo" || channel != "channel" || destination != p.parent {
		return ShelfDownload{}, errors.New("wrong scope")
	}
	p.ids = append(p.ids, upload)
	if p.cancel != nil {
		p.cancel()
	}
	if p.failSecond && len(p.ids) == 2 {
		return ShelfDownload{}, errors.New("fixture destination collision")
	}
	return ShelfDownload{OperationID: "operation", FetchID: fmt.Sprint(len(p.ids)), State: "queued"}, nil
}
func (p *batchShelfProbe) ShelfFetchStatus(context.Context, string) (ShelfDownload, error) {
	return ShelfDownload{OperationID: "operation", FetchID: fmt.Sprint(len(p.ids)), State: "complete"}, nil
}

func TestShelfBatchStopsOnFailureAndKeepsDisplayedSelection(t *testing.T) {
	for _, mode := range []string{"complete", "partial-error", "cancel-confirmation", "cancel-transfer", "denied"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := &batchShelfProbe{parent: t.TempDir(), failSecond: mode == "partial-error", deny: mode == "denied"}
			if mode == "cancel-transfer" {
				p.cancel = cancel
			}
			ui := &platformtest.Fake{ConfirmFunc: func(_ context.Context, r platform.ConfirmRequest) (bool, error) {
				if r.PresentationKey != "confirm.shelfImportAll" || r.PresentationArgs["count"] != "3" {
					t.Error("missing bounded consent", r)
				}
				return mode != "cancel-confirmation", nil
			}}
			c := New(Config{Shelf: p, Prompter: ui, Notifier: ui})
			c.importShelfItems(ctx, "batch", "server", "repo", "channel", "shelf", []platform.ShelfItem{{UploadID: "one"}, {UploadID: "two"}, {UploadID: "three"}})
			want := map[string]int{"complete": 3, "partial-error": 2, "cancel-confirmation": 0, "cancel-transfer": 1, "denied": 0}[mode]
			if len(p.ids) != want || p.listed != 0 {
				t.Fatal("selection expanded or continued after stop", p.ids, p.listed)
			}
			if mode == "partial-error" || mode == "denied" {
				notices := ui.Snapshot().Notifications
				wantCount := "1 / 3"
				if mode == "denied" {
					wantCount = "0 / 3"
				}
				if len(notices) == 0 || notices[len(notices)-1].Body != wantCount {
					t.Fatal("missing partial result", notices)
				}
				if len(ui.Snapshot().InfoRequests) == 0 {
					t.Fatal("silent failure")
				}
			}
		})
	}
}

func TestShelfRefreshRechecksAvailabilityAndRejectsForeignSelection(t *testing.T) {
	p := &batchShelfProbe{parent: t.TempDir(), deny: true}
	ui := &platformtest.Fake{}
	views := 0
	ui.ShelfFunc = func(_ context.Context, r platform.ShelfDialogRequest) (platform.ShelfDialogResult, error) {
		views++
		if views == 1 {
			if r.CanImport {
				t.Fatal("unexpected authority")
			}
			p.deny = false
			return platform.ShelfDialogResult{Action: platform.ShelfDialogRefresh}, nil
		}
		if !r.CanImport {
			t.Fatal("availability was not refreshed")
		}
		return platform.ShelfDialogResult{Action: platform.ShelfDialogImport, UploadID: "not-displayed"}, nil
	}
	c := New(Config{Shelf: p, ShelfBrowser: ui, Prompter: ui, Notifier: ui})
	c.showShelf(t.Context(), "server", "repo", UploadChannelSummary{ChannelID: "channel", Slug: "shelf"})
	if views != 2 || p.listed != 2 || len(p.ids) != 0 {
		t.Fatal(views, p.listed, p.ids)
	}
	if len(ui.Snapshot().InfoRequests) == 0 {
		t.Fatal("foreign selection silently ignored")
	}
}
