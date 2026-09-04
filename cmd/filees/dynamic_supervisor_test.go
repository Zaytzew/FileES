package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
)

type projectionSVN struct {
	client.Client
	checkouts, updates int
	url, wc            string
}

func (svn *projectionSVN) Checkout(_ context.Context, url, wc string) (string, error) {
	svn.checkouts++
	svn.url, svn.wc = url, wc
	return "checked out", nil
}

func (svn *projectionSVN) Update(_ context.Context, wc string) (string, error) {
	svn.updates++
	svn.wc = wc
	return "updated", nil
}

func TestServiceProjectionUpdaterChecksOutMissingWorkingCopyThenUpdates(t *testing.T) {
	wc := filepath.Join(t.TempDir(), "service-wc")
	svn := &projectionSVN{}
	updater := serviceProjectionUpdater{client: svn, url: "svn+ssh://_filees-client@example.net/"}
	if _, err := updater.Update(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if svn.checkouts != 1 || svn.updates != 0 || svn.url != updater.url || svn.wc != wc {
		t.Fatalf("checkout=%d update=%d url=%q wc=%q", svn.checkouts, svn.updates, svn.url, svn.wc)
	}
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := updater.Update(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if svn.checkouts != 1 || svn.updates != 1 {
		t.Fatalf("checkout=%d update=%d", svn.checkouts, svn.updates)
	}
}

type fakePublicShareLister struct {
	calls []string // "serverID/repoID" per call, in order
	err   map[string]error
}

func (f *fakePublicShareLister) ListPublicShares(_ context.Context, serverID, repoID string) ([]contract.PublicShareSummary, error) {
	f.calls = append(f.calls, serverID+"/"+repoID)
	if err := f.err[repoID]; err != nil {
		return nil, err
	}
	return []contract.PublicShareSummary{{ChannelID: "ch-" + repoID}}, nil
}

func TestRefreshPublicSharesOnlyListsOwnedActiveRepos(t *testing.T) {
	realmID := "11111111-1111-1111-1111-111111111111"
	view := clientview.View{
		RealmID: realmID,
		Repositories: []clientview.Repository{
			{RepoID: "owned-active", DisplayName: "Owned Active", OwnerRealmID: realmID, State: "active"},
			{RepoID: "owned-disabled", DisplayName: "Owned Disabled", OwnerRealmID: realmID, State: "disabled"},
			{RepoID: "foreign", DisplayName: "Foreign", OwnerRealmID: "22222222-2222-2222-2222-222222222222", State: "active"},
		},
	}
	lister := &fakePublicShareLister{}
	cache := newPublicShareCache()
	refreshPublicShares(t.Context(), lister, cache, nil, "srv-1", view)

	if len(lister.calls) != 1 || lister.calls[0] != "srv-1/owned-active" {
		t.Fatalf("calls = %v, want exactly one call for the owned active repo", lister.calls)
	}
	got := cache.List()
	if len(got) != 1 || got[0].ChannelID != "ch-owned-active" || got[0].ServerID != "srv-1" || got[0].RepoDisplayName != "Owned Active" {
		t.Fatalf("cache.List() = %+v, want one stamped share for the owned active repo", got)
	}
}

func TestRefreshPublicSharesSkipsFailingRepoAndKeepsOthers(t *testing.T) {
	realmID := "11111111-1111-1111-1111-111111111111"
	view := clientview.View{
		RealmID: realmID,
		Repositories: []clientview.Repository{
			{RepoID: "good", DisplayName: "Good", OwnerRealmID: realmID, State: "active"},
			{RepoID: "bad", DisplayName: "Bad", OwnerRealmID: realmID, State: "active"},
		},
	}
	lister := &fakePublicShareLister{err: map[string]error{"bad": errors.New("control-plane exchange failed")}}
	cache := newPublicShareCache()
	refreshPublicShares(t.Context(), lister, cache, nil, "srv-1", view)

	got := cache.List()
	if len(got) != 1 || got[0].ChannelID != "ch-good" {
		t.Fatalf("cache.List() = %+v, want only the successfully-listed repo's share", got)
	}
}

func TestRefreshPublicSharesNoopsWithoutRealmID(t *testing.T) {
	lister := &fakePublicShareLister{}
	cache := newPublicShareCache()
	refreshPublicShares(t.Context(), lister, cache, nil, "srv-1", clientview.View{})
	if len(lister.calls) != 0 {
		t.Fatalf("expected no calls for a view without a RealmID, got %v", lister.calls)
	}
}

type blockingPublicShareLister struct {
	calls   chan string
	release chan struct{}
}

func (lister *blockingPublicShareLister) ListPublicShares(_ context.Context, serverID, repoID string) ([]contract.PublicShareSummary, error) {
	lister.calls <- serverID + "/" + repoID
	<-lister.release
	return []contract.PublicShareSummary{{ChannelID: "ch-" + repoID}}, nil
}

func TestPublicShareRefreshCoordinatorRerunsMutationArrivingDuringRefresh(t *testing.T) {
	realmID := "11111111-1111-1111-1111-111111111111"
	view := func(repoID string) clientview.View {
		return clientview.View{RealmID: realmID, Repositories: []clientview.Repository{{RepoID: repoID, DisplayName: repoID, OwnerRealmID: realmID, State: "active"}}}
	}
	lister := &blockingPublicShareLister{calls: make(chan string, 3), release: make(chan struct{})}
	cache := newPublicShareCache()
	coordinator := newPublicShareRefreshCoordinator(t.Context(), lister, cache, nil)
	coordinator.Schedule("srv-1", view("before-mutation"))
	select {
	case call := <-lister.calls:
		if call != "srv-1/before-mutation" {
			t.Fatalf("first call = %q", call)
		}
	case <-time.After(time.Second):
		t.Fatal("initial cached-view refresh did not start")
	}

	coordinator.Schedule("srv-1", view("after-mutation"))
	close(lister.release)
	select {
	case call := <-lister.calls:
		if call != "srv-1/after-mutation" {
			t.Fatalf("rerun call = %q", call)
		}
	case <-time.After(time.Second):
		t.Fatal("mutation refresh was dropped while the initial refresh was running")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		shares := cache.List()
		if len(shares) == 1 && shares[0].ChannelID == "ch-after-mutation" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("final cache = %+v", cache.List())
}

func TestUnchangedServerGenerationCanPublishLocalLifecycleFailure(t *testing.T) {
	serverID := "spot"
	repoID := "00000000-0000-0000-0000-000000000021"
	view := clientview.View{Generation: 7, Repositories: []clientview.Repository{{
		RepoID: repoID, DisplayName: "M21 live", URL: "svn+ssh://_filees-data@example/" + repoID,
		Access: contract.AccessReadWrite, State: contract.StateInitializing,
	}}}
	lifecycle, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := lifecycle.BeginCreate(serverID, "M21 live", filepath.Join(t.TempDir(), "M21-live"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.MarkRepositoryCreated(record.OperationID, repoID, view.Repositories[0].URL); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.MarkError(record.OperationID, errors.New("initial import failed")); err != nil {
		t.Fatal(err)
	}

	ipc := ipcserver.New(filepath.Join(t.TempDir(), "daemon.sock"))
	state := ipc.RegisterProjectedRepo(repoID, "M21 live", view.Repositories[0].URL, serverID, contract.AccessReadWrite, contract.StateInitializing, false)
	currentViews := map[string]clientview.View{serverID: view}
	synced := projectionUpdate{serverID: serverID, view: view}
	if !syncProjectionOnSuccessfulPoll(ipc, synced, currentViews, map[reposupervisor.Key]repoRuntime{}, lifecycle) {
		t.Fatal("unchanged successful sync was not recognised as the current generation")
	}

	summary := state.Summary()
	if summary.LifecycleOperationID != record.OperationID || !summary.CanRetryLifecycle || !summary.CanAbandonLifecycle || summary.State != contract.StateInteractionRequired {
		t.Fatalf("unchanged generation did not publish local repair state: %+v", summary)
	}
}
