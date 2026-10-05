package mobileclient

import (
	"context"
	"encoding/json"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

type scriptedTransport struct {
	fn func(v1.Request) (v1.Response, []byte, error)
}

func (s scriptedTransport) Do(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
	return s.fn(req)
}

func scrambledPage(rev int64) v1.Manifest {
	return v1.Manifest{
		Schema:         v1.ManifestSchema,
		RepoID:         "repo-1",
		ViewGeneration: 2,
		RepoRevision:   rev,
		Entries: []v1.ManifestEntry{
			{Path: "b.txt", Kind: v1.KindFile, LastChangedRevision: 1},
			{Path: "a.txt", Kind: v1.KindFile, LastChangedRevision: 3},
			{Path: "m", Kind: v1.KindDirectory, LastChangedRevision: 2},
		},
	}
}

func assertPathOrder(t *testing.T, entries []v1.ManifestEntry) {
	t.Helper()
	want := []string{"m", "a.txt", "b.txt"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for i, path := range want {
		if entries[i].Path != path {
			t.Fatalf("entry %d = %s, want %s (arrival was b.txt, a.txt, m; revision order was a.txt, m, b.txt)", i, entries[i].Path, path)
		}
	}
}

func TestListDirectoryStoresPathOrder(t *testing.T) {
	page := scrambledPage(4)
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	lists := 0
	c := Client{Store: Store{Root: t.TempDir()}, Transport: scriptedTransport{fn: func(req v1.Request) (v1.Response, []byte, error) {
		if req.Operation != v1.OpListDirectory {
			t.Fatalf("operation %s", req.Operation)
		}
		lists++
		return v1.Response{Status: v1.StatusOK, Result: raw}, nil, nil
	}}}
	got, err := c.ListDirectory(context.Background(), "repo-1", "", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	assertPathOrder(t, got.Entries)
	again, err := c.ListDirectory(context.Background(), "repo-1", "", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	assertPathOrder(t, again.Entries)
	if lists != 1 {
		t.Fatalf("server lists = %d, want 1", lists)
	}
	newest, err := c.CachedDirectory("repo-1", "")
	if err != nil || newest == nil {
		t.Fatalf("newest = %+v err=%v", newest, err)
	}
	assertPathOrder(t, newest.Entries)
}

func TestCachedDirectoryRewritesWireOrderWithoutNetwork(t *testing.T) {
	store := Store{Root: t.TempDir()}
	wire := directoryListing{
		Schema:         dirListingSchema,
		RepoID:         "repo-1",
		Path:           "",
		ViewGeneration: 2,
		RepoRevision:   4,
		Entries:        scrambledPage(4).Entries,
	}
	if err := atomicWriteJSON(store.dirListingPath("repo-1", "", 2, 4), wire); err != nil {
		t.Fatal(err)
	}
	c := Client{Store: store, Transport: scriptedTransport{fn: func(v1.Request) (v1.Response, []byte, error) {
		t.Fatal("a cached page is the browse source")
		return v1.Response{}, nil, nil
	}}}
	got, err := c.CachedDirectory("repo-1", "")
	if err != nil {
		t.Fatal(err)
	}
	assertPathOrder(t, got.Entries)
	stored, err := store.LoadDirectory("repo-1", "", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	assertPathOrder(t, stored.Entries)
}

func TestSyncDirectoryKeepsLocalPageWhenHeaderUnchanged(t *testing.T) {
	store := Store{Root: t.TempDir()}
	header := &v1.Manifest{Schema: v1.ManifestSchema, RepoID: "repo-1", ViewGeneration: 2, RepoRevision: 4}
	if _, err := store.SaveManifestIfNewer(header); err != nil {
		t.Fatal(err)
	}
	page := scrambledPage(4)
	if err := store.SaveDirectory("", &page); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := Client{Store: store, Transport: scriptedTransport{fn: func(req v1.Request) (v1.Response, []byte, error) {
		calls++
		if req.Operation != v1.OpRefreshManifest {
			t.Fatalf("operation %s", req.Operation)
		}
		raw, _ := json.Marshal(v1.RefreshManifestResult{NotModified: true})
		return v1.Response{Status: v1.StatusOK, Result: raw}, nil, nil
	}}}
	if _, err := c.SyncDirectory(context.Background(), "repo-1", "", 2, 4, false); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("confirmed page made %d server calls", calls)
	}
	got, err := c.SyncDirectory(context.Background(), "repo-1", "", 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	assertPathOrder(t, got.Entries)
	if calls != 1 {
		t.Fatalf("header check made %d calls, want 1", calls)
	}
}

func TestSyncDirectoryListsOnlyWhenPageIsBehind(t *testing.T) {
	store := Store{Root: t.TempDir()}
	header := &v1.Manifest{Schema: v1.ManifestSchema, RepoID: "repo-1", ViewGeneration: 2, RepoRevision: 4}
	if _, err := store.SaveManifestIfNewer(header); err != nil {
		t.Fatal(err)
	}
	old := scrambledPage(4)
	if err := store.SaveDirectory("", &old); err != nil {
		t.Fatal(err)
	}
	moved := &v1.Manifest{
		Schema: v1.ManifestSchema, RepoID: "repo-1", ViewGeneration: 2, RepoRevision: 5,
		Shouts: []v1.ShoutNotice{{Revision: 5, Comment: "gotowe"}},
	}
	listed := scrambledPage(5)
	var ops []v1.Operation
	c := Client{Store: store, Transport: scriptedTransport{fn: func(req v1.Request) (v1.Response, []byte, error) {
		ops = append(ops, req.Operation)
		switch req.Operation {
		case v1.OpRefreshManifest:
			raw, _ := json.Marshal(v1.RefreshManifestResult{Manifest: moved})
			return v1.Response{Status: v1.StatusOK, Result: raw}, nil, nil
		case v1.OpListDirectory:
			raw, _ := json.Marshal(listed)
			return v1.Response{Status: v1.StatusOK, Result: raw}, nil, nil
		default:
			t.Fatalf("operation %s", req.Operation)
			return v1.Response{}, nil, nil
		}
	}}}
	got, err := c.SyncDirectory(context.Background(), "repo-1", "photos", 2, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0] != v1.OpListDirectory {
		t.Fatalf("missing page ops = %v", ops)
	}
	ops = nil
	got, err = c.SyncDirectory(context.Background(), "repo-1", "", 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0] != v1.OpRefreshManifest || ops[1] != v1.OpListDirectory {
		t.Fatalf("behind page ops = %v", ops)
	}
	assertPathOrder(t, got.Entries)
	if len(got.Shouts) != 1 || got.Shouts[0].Comment != "gotowe" {
		t.Fatalf("shouts = %+v", got.Shouts)
	}
	stored, err := store.LoadDirectory("repo-1", "", 2, 5)
	if err != nil || stored == nil {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
	assertPathOrder(t, stored.Entries)
	if !sameDirectoryPage(stored, &listed) {
		t.Fatal("stored page differs from the server page beyond order")
	}
}
