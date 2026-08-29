package main

import (
	"testing"

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
