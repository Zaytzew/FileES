package actions_test

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type dashboardPublicShareManager struct {
	shares  []actions.PublicShareSummary
	revoked chan string
}

func (manager *dashboardPublicShareManager) ListPublicShares(context.Context, string, string) ([]actions.PublicShareSummary, error) {
	return append([]actions.PublicShareSummary(nil), manager.shares...), nil
}
func (*dashboardPublicShareManager) CreatePublicShare(context.Context, string, actions.PublicShareDeclaration) error {
	return nil
}
func (*dashboardPublicShareManager) UpdatePublicShare(context.Context, string, string, actions.PublicShareDeclaration) error {
	return nil
}
func (manager *dashboardPublicShareManager) RevokePublicShare(_ context.Context, serverID, repoID, channelID string) error {
	manager.revoked <- serverID + "/" + repoID + "/" + channelID
	return nil
}
func (*dashboardPublicShareManager) DeletePublicShare(context.Context, string, string, string) error {
	return nil
}

func publicShareDashboardView() app.ViewModel {
	view := lifecycleView(
		contract.CapRepoPublicShareList, contract.CapRepoPublicShareCreate,
		contract.CapRepoPublicShareUpdate, contract.CapRepoPublicShareRevoke,
		contract.CapRepoPublicShareDelete,
	)
	view.PublicSharesKnown = true
	view.PublicShares = []app.PublicShareViewModel{{ChannelID: "share-1", ServerID: "office", RepoID: "repo-1", State: "active"}}
	return view
}

func TestControllerOpensFocusedPublicShareDirectlyFromDashboard(t *testing.T) {
	manager := &dashboardPublicShareManager{shares: []actions.PublicShareSummary{{ChannelID: "share-1", State: "active"}}, revoked: make(chan string, 1)}
	platformFake := &platformtest.Fake{
		PublicSharesFunc: func(_ context.Context, request platform.PublicShareDialogRequest) (platform.PublicShareDialogResult, error) {
			if !request.DirectEntry || request.FocusChannelID != "share-1" || request.ServerID != "office" || request.RepoID != "repo-1" {
				t.Fatalf("direct public share request = %#v", request)
			}
			return platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}, nil
		},
	}
	view := publicShareDashboardView()
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, PublicShares: manager,
		PublicShareBrowser: platformFake, FolderPicker: platformFake, Prompter: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentManagePublicShares, ServerID: "office", RepoID: "repo-1", ChannelID: "share-1"})
	deadline := time.Now().Add(time.Second)
	for len(platformFake.Snapshot().PublicShareRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(platformFake.Snapshot().PublicShareRequests) != 1 {
		t.Fatal("direct public share browser was not opened")
	}
}

func TestControllerRevalidatesAndRevokesDashboardPublicShare(t *testing.T) {
	manager := &dashboardPublicShareManager{revoked: make(chan string, 1)}
	platformFake := &platformtest.Fake{ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil }}
	view := publicShareDashboardView()
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, PublicShares: manager,
		Prompter: platformFake, Notifier: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentRevokePublicShare, ServerID: "office", RepoID: "repo-1", ChannelID: "share-1"})
	select {
	case got := <-manager.revoked:
		if got != "office/repo-1/share-1" {
			t.Fatalf("revoked = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("public share was not revoked")
	}
}

func TestControllerBulkRevokesOneServerAndRefreshesProjection(t *testing.T) {
	manager := &dashboardPublicShareManager{revoked: make(chan string, 2)}
	platformFake := &platformtest.Fake{ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
		if request.Title != "Cofnij udostępnienia" {
			t.Fatalf("confirmation = %#v", request)
		}
		return true, nil
	}}
	view := publicShareDashboardView()
	view.PublicShares = append(view.PublicShares, app.PublicShareViewModel{ChannelID: "share-2", ServerID: "office", RepoID: "repo-1", State: "active"})
	refreshed := make(chan struct{}, 1)
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, PublicShares: manager,
		Prompter: platformFake, Notifier: platformFake, Refresh: func() { refreshed <- struct{}{} },
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentRevokePublicShares, ServerID: "office", ChannelIDs: []string{"share-1", "share-2"}})
	want := map[string]bool{"office/repo-1/share-1": true, "office/repo-1/share-2": true}
	for range 2 {
		select {
		case got := <-manager.revoked:
			if !want[got] {
				t.Fatalf("unexpected bulk revoke = %q", got)
			}
			delete(want, got)
		case <-time.After(time.Second):
			t.Fatal("bulk public share revoke timed out")
		}
	}
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("bulk revoke did not request a fresh projection")
	}
}
