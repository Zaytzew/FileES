package main

import (
	"context"
	"errors"
	"testing"

	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/publicstate"
	"filees/pkg/reservationclient"
	"github.com/google/uuid"
)

type stateLinksFixture struct {
	binding  publicstate.Binding
	items    []publicstate.Item
	requests []publicstate.Request
	legacy   int
	err      error
}

func (f *stateLinksFixture) ListPublicShares(context.Context, string, string) ([]contract.PublicShareSummary, error) {
	f.legacy++
	return nil, nil
}
func (f *stateLinksFixture) FetchPublicLinks(_ context.Context, _ string, r publicstate.Request) (publicstate.Response, error) {
	f.requests = append(f.requests, r)
	if f.err != nil {
		return publicstate.Response{}, f.err
	}
	return publicstate.Build(f.binding, r, f.items)
}

func TestPublicLinksRefreshUsesServerInventoryAndRetainsCacheOnFailure(t *testing.T) {
	f := &stateLinksFixture{binding: publicstate.Binding{ServerID: "lab", ClientID: uuid.NewString(), RealmID: uuid.NewString()}}
	f.items = []publicstate.Item{{RepoDisplayName: "server name", Share: control.PublicShareSummary{ChannelID: uuid.NewString(), RepoID: uuid.NewString(), State: "active"}}}
	cache := newPublicShareCache()
	cache.Bind("lab", f.binding.ClientID, "host", 22, "")
	mirror := &publicLinksMirror{}
	view := clientview.View{RealmID: f.binding.RealmID} // deliberately no local repo inventory
	run := func() { refreshPublicShares(t.Context(), f, cache, nil, "lab", view, mirror) }
	run()
	run()
	if f.legacy != 0 || len(f.requests) != 2 || len(f.requests[0].Known) != 0 || len(f.requests[1].Known) != 1 {
		t.Fatalf("requests=%+v legacy=%d", f.requests, f.legacy)
	}
	snapshot := cache.Snapshot()
	if len(snapshot.Shares) != 1 || snapshot.Stale || snapshot.Loading || snapshot.Shares[0].RepoDisplayName != "server name" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	f.err = errors.New("transport unavailable")
	run()
	if got := cache.Snapshot(); len(got.Shares) != 1 || !got.Stale || f.legacy != 0 {
		t.Fatalf("failed attempt lost cache: %+v", got)
	}
	f.err = nil
	f.items = nil
	run()
	if got := cache.Snapshot(); len(got.Shares) != 0 || got.Stale {
		t.Fatalf("deletion: %+v", got)
	}
	cache.Bind("lab", uuid.NewString(), "host", 22, "")
	run()
	if len(f.requests[len(f.requests)-1].Known) != 0 {
		t.Fatal("activation reused previous mirror")
	}
}

func TestPublicLinksFallbackOnlyOnExplicitUnsupported(t *testing.T) {
	f := &stateLinksFixture{binding: publicstate.Binding{ServerID: "lab", ClientID: uuid.NewString(), RealmID: uuid.NewString()}, err: reservationclient.ErrPublicLinksUnsupported}
	cache := newPublicShareCache()
	mirror := &publicLinksMirror{}
	view := clientview.View{RealmID: f.binding.RealmID, Repositories: []clientview.Repository{{RepoID: uuid.NewString(), State: "active", OwnerRealmID: f.binding.RealmID}}}
	for i := 0; i < 2; i++ {
		refreshPublicShares(t.Context(), f, cache, nil, "lab", view, mirror)
	}
	if len(f.requests) != 1 || f.legacy != 2 {
		t.Fatalf("requests=%d legacy=%d", len(f.requests), f.legacy)
	}
}
