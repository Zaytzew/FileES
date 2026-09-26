package main

import (
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

func TestPublicShareCacheListFlattensAndSortsAcrossServers(t *testing.T) {
	c := newPublicShareCache()
	c.Set("srv-b", []contract.PublicShareSummary{{ChannelID: "ch2", ServerID: "srv-b"}, {ChannelID: "ch1", ServerID: "srv-b"}})
	c.Set("srv-a", []contract.PublicShareSummary{{ChannelID: "ch9", ServerID: "srv-a"}})

	got := c.List()
	if len(got) != 3 {
		t.Fatalf("List() len = %d, want 3", len(got))
	}
	want := []string{"srv-a/ch9", "srv-b/ch1", "srv-b/ch2"}
	for i, w := range want {
		if key := got[i].ServerID + "/" + got[i].ChannelID; key != w {
			t.Fatalf("List()[%d] = %q, want %q (full: %+v)", i, key, w, got[i])
		}
	}
}

func TestPublicShareCacheSetEmptyRemovesServerEntry(t *testing.T) {
	c := newPublicShareCache()
	c.Set("srv-a", []contract.PublicShareSummary{{ChannelID: "ch1", ServerID: "srv-a"}})
	if len(c.List()) != 1 {
		t.Fatalf("expected one cached share before clearing")
	}
	c.Set("srv-a", nil)
	if got := c.List(); len(got) != 0 {
		t.Fatalf("List() after clearing = %+v, want empty", got)
	}
}

func TestPublicShareCacheRetainsFailedRepoAndShowsStaleness(t *testing.T) {
	c := newPublicShareCache()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.Set("server", []contract.PublicShareSummary{{RepoID: "good", ChannelID: "a"}, {RepoID: "bad", ChannelID: "b"}})
	first := c.Snapshot()
	if first.Stale || first.AsOf == "" || first.Generation == 0 {
		t.Fatal(first)
	}
	now = now.Add(time.Minute)
	c.SetPartial("server", []contract.PublicShareSummary{{RepoID: "good", ChannelID: "new-a"}}, []string{"bad"})
	partial := c.Snapshot()
	if !partial.Stale || len(partial.Shares) != 2 || partial.AsOf != first.AsOf || partial.Generation <= first.Generation {
		t.Fatal(partial)
	}
	if partial.Shares[0].ChannelID != "b" || !partial.Shares[0].Stale || partial.Shares[0].ObservedAt != first.Shares[1].ObservedAt {
		t.Fatal(partial)
	}
	if partial.Shares[1].Stale || partial.Shares[1].ObservedAt == first.Shares[0].ObservedAt {
		t.Fatalf("fresh sibling repo was marked stale: %+v", partial.Shares[1])
	}
	c.Set("server", []contract.PublicShareSummary{{RepoID: "good", ChannelID: "new-a"}})
	if snapshot := c.Snapshot(); snapshot.Stale || len(snapshot.Shares) != 1 {
		t.Fatal(snapshot)
	}
	now = now.Add(6 * time.Minute)
	if snapshot := c.Snapshot(); !snapshot.Stale || !snapshot.Shares[0].Stale {
		t.Fatal(snapshot)
	}
	c.SetDetached("server", true)
	c.Set("server", first.Shares) // late reply from an already detached session
	if snapshot := c.Snapshot(); len(snapshot.Shares) != 0 {
		t.Fatal(snapshot)
	}
}

func TestPublicShareCacheUnreadServerIsNotComplete(t *testing.T) {
	c := newPublicShareCache()
	c.Track("unread")
	c.Set("fresh", nil)
	for i := 0; i < 20; i++ {
		got := c.Snapshot()
		if !got.Stale || got.AsOf != "" {
			t.Fatalf("unread server was reported complete: %+v", got)
		}
	}
	c.Set("unread", nil)
	if got := c.Snapshot(); got.Stale || got.AsOf == "" {
		t.Fatalf("complete empty aggregate: %+v", got)
	}
}

// A disconnected lab used to turn every current production link stale.
func TestPublicShareCacheKeepsFreshRowsWhenAnotherServerIsUnreadOrExpired(t *testing.T) {
	for _, unread := range []bool{true, false} {
		t.Run(map[bool]string{true: "unread", false: "expired"}[unread], func(t *testing.T) {
			c := newPublicShareCache()
			now := time.Date(2026, 9, 26, 20, 0, 0, 0, time.UTC)
			c.now = func() time.Time { return now }
			if unread {
				c.Track("lab")
			} else {
				c.Set("lab", []contract.PublicShareSummary{{ChannelID: "lab-link", ServerID: "lab"}})
			}
			now = now.Add(6 * time.Minute)
			c.Set("production", []contract.PublicShareSummary{{ChannelID: "live-link", ServerID: "production"}})
			got := c.Snapshot()
			if !got.Stale {
				t.Fatal("incomplete aggregate presented as current")
			}
			for _, row := range got.Shares {
				if row.Stale != (row.ServerID == "lab") {
					t.Fatalf("wrong row freshness: %+v", row)
				}
			}
		})
	}
}

func TestPublicShareCachePartialRefreshAgesEachRowAndClearsFailureOnSuccess(t *testing.T) {
	c := newPublicShareCache()
	now := time.Date(2026, 9, 26, 20, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	rows := []contract.PublicShareSummary{{RepoID: "a", ChannelID: "a"}, {RepoID: "b", ChannelID: "b"}}
	c.Set("server", rows)
	now = now.Add(6 * time.Minute)
	c.SetPartial("server", rows[:1], []string{"b"})
	got := c.Snapshot()
	if !got.Stale || got.Shares[0].Stale || !got.Shares[1].Stale {
		t.Fatalf("partial: %+v", got)
	}
	// A successful observation supersedes an old stale flag as well as its date.
	c.Set("server", got.Shares)
	got = c.Snapshot()
	if got.Stale || got.Shares[0].Stale || got.Shares[1].Stale {
		t.Fatalf("recovered: %+v", got)
	}
	now = now.Add(6 * time.Minute)
	got = c.Snapshot()
	if !got.Stale || !got.Shares[0].Stale || !got.Shares[1].Stale {
		t.Fatalf("expired: %+v", got)
	}
}
