package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/privatefile"
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

func privateShareCachePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := privatefile.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "view.json.public-shares.json")
}

func TestPublicShareCacheRestoresImmediatelyButUnverified(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "links", true: "verified-empty"}[empty], func(t *testing.T) {
			path := privateShareCachePath(t)
			c := newPublicShareCache()
			c.Bind("server", "client", "host", 2224, path)
			rows := []contract.PublicShareSummary{{ServerID: "server", RepoID: "repo", ChannelID: "link"}}
			if empty {
				rows = nil
			}
			c.Set("server", rows)
			first := c.Snapshot()
			if err := privatefile.Verify(path); err != nil {
				t.Fatal(err)
			}
			restarted := newPublicShareCache()
			restarted.Bind("server", "client", "host", 2224, path)
			got := restarted.Snapshot()
			if len(got.Shares) != len(rows) || !got.Stale || got.Loading || got.AsOf != first.AsOf {
				t.Fatalf("restart: %+v", got)
			}
			for _, row := range got.Shares {
				if !row.Stale {
					t.Fatal("restored row claims freshness")
				}
			}
			epoch := restarted.BeginRefresh("server")
			if !restarted.Snapshot().Loading {
				t.Fatal("loading lost")
			}
			restarted.CompleteRefresh("server", epoch, nil, []string{"repo"})
			restarted.EndRefresh("server", epoch)
			if got := restarted.Snapshot(); got.Loading || len(got.Shares) != len(rows) {
				t.Fatalf("failed read erased cache: %+v", got)
			}
			restarted.Set("server", nil)
			if got := restarted.Snapshot(); got.Stale || len(got.Shares) != 0 {
				t.Fatalf("confirmed empty: %+v", got)
			}
		})
	}
}

func TestPublicShareCacheRejectsOtherActivationAndLateReply(t *testing.T) {
	path := privateShareCachePath(t)
	c := newPublicShareCache()
	c.Bind("server", "old-client", "host", 2224, path)
	c.Set("server", []contract.PublicShareSummary{{ServerID: "server", RepoID: "repo", ChannelID: "old"}})
	epoch := c.BeginRefresh("server")
	c.Bind("server", "new-client", "host", 2224, path)
	c.CompleteRefresh("server", epoch, []contract.PublicShareSummary{{ServerID: "server", ChannelID: "late"}}, nil)
	c.EndRefresh("server", epoch)
	if got := c.Snapshot(); len(got.Shares) != 0 || got.Loading {
		t.Fatalf("old activation leaked: %+v", got)
	}
	c.Set("server", []contract.PublicShareSummary{{ServerID: "server", RepoID: "repo", ChannelID: "new"}})
	epoch = c.BeginRefresh("server")
	c.SetDetached("server", true)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("detached cache remains: %v", err)
	}
	c.SetDetached("server", false)
	c.Bind("server", "third-client", "host", 2224, path)
	c.CompleteRefresh("server", epoch, []contract.PublicShareSummary{{ServerID: "server", ChannelID: "late"}}, nil)
	if got := c.Snapshot(); len(got.Shares) != 0 {
		t.Fatalf("late detached reply leaked: %+v", got)
	}
}

func TestPublicShareCacheBindingRejectsAnotherHostPortAndServer(t *testing.T) {
	path := privateShareCachePath(t)
	c := newPublicShareCache()
	c.Bind("server", "client", "host", 2224, path)
	c.Set("server", []contract.PublicShareSummary{{ServerID: "server", RepoID: "repo", ChannelID: "saved"}})
	for _, b := range []publicShareCacheBinding{{ServerID: "server", ClientID: "client", Address: "other", Port: 2224}, {ServerID: "server", ClientID: "client", Address: "host", Port: 22}, {ServerID: "other", ClientID: "client", Address: "host", Port: 2224}} {
		other := newPublicShareCache()
		other.Bind(b.ServerID, b.ClientID, b.Address, b.Port, path)
		if len(other.Snapshot().Shares) != 0 {
			t.Fatal("mismatched binding restored")
		}
	}
}

func TestPublicShareCacheSizeAndSchemaFailuresAreDisposable(t *testing.T) {
	path := privateShareCachePath(t)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(publicShareCacheLimit + 1); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = privatefile.Harden(path); err != nil {
		t.Fatal(err)
	}
	c := newPublicShareCache()
	c.Bind("server", "client", "host", 2224, path)
	if len(c.Snapshot().Shares) != 0 {
		t.Fatal("oversized cache accepted")
	}
	saved := publicShareCacheFile{Schema: 99, Binding: publicShareCacheBinding{ServerID: "server", ClientID: "client", Address: "host", Port: 2224}, Shares: []contract.PublicShareSummary{{ServerID: "server", ChannelID: "future"}}}
	if err := writePublicShareCache(path, saved); err != nil {
		t.Fatal(err)
	}
	c = newPublicShareCache()
	c.Bind("server", "client", "host", 2224, path)
	if len(c.Snapshot().Shares) != 0 {
		t.Fatal("unknown schema accepted")
	}
}

func TestPublicShareCacheCorruptOrUnwritableDoesNotBreakLiveRead(t *testing.T) {
	path := privateShareCachePath(t)
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := privatefile.Harden(path); err != nil {
		t.Fatal(err)
	}
	c := newPublicShareCache()
	c.Bind("server", "client", "host", 2224, path)
	if len(c.Snapshot().Shares) != 0 {
		t.Fatal("corrupt cache accepted")
	}
	c.Set("server", []contract.PublicShareSummary{{ServerID: "server", ChannelID: "live"}})
	if len(c.Snapshot().Shares) != 1 {
		t.Fatal("live read blocked by corrupt cache")
	}
	c.Bind("server", "client", "host", 2224, filepath.Join(path, "no-directory"))
	c.Set("server", []contract.PublicShareSummary{{ServerID: "server", ChannelID: "still-live"}})
	if len(c.Snapshot().Shares) != 1 {
		t.Fatal("cache save failure broke live read")
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
