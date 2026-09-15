package main

import (
	"testing"

	"filees/pkg/client"
	"filees/pkg/shout"
)

func TestHistoryURLEscapesEachSegment(t *testing.T) {
	for path, want := range map[string]string{
		"":                         "svn://office/projects",
		"01_EDITABLES":             "svn://office/projects/01_EDITABLES",
		"2026 wiosna/opis #1?.txt": "svn://office/projects/2026%20wiosna/opis%20%231%3F.txt",
		"Zażółć":                   "svn://office/projects/Za%C5%BC%C3%B3%C5%82%C4%87",
	} {
		if got := historyURL("svn://office/projects/", path); got != want {
			t.Errorf("historyURL(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestHistoryLogEntriesShowOnlyShouts(t *testing.T) {
	entries := historyLogEntries([]client.HistoryCommit{
		{Revision: 3, Message: shout.Format("nowe rzuty"), Changes: []client.HistoryChange{{Path: "NEW", Action: "A", CopyFromPath: "OLD", CopyFromRevision: 2}}},
		{Revision: 2, Message: "filees: automatic sync", Changes: []client.HistoryChange{{Path: "OLD", Action: "M", CopyFromRevision: -1}}},
	})
	if entries[0].Shout != "nowe rzuty" || entries[1].Shout != "" {
		t.Fatalf("shouts = %q / %q", entries[0].Shout, entries[1].Shout)
	}
	if copied := entries[0].Changes[0]; copied.CopyFromPath != "OLD" || copied.CopyFromRevision != 2 {
		t.Fatalf("copy source lost: %+v", copied)
	}
	if plain := entries[1].Changes[0]; plain.CopyFromPath != "" || plain.CopyFromRevision != 0 {
		t.Fatalf("plain change grew a copy source: %+v", plain)
	}
}
