package ipcserver

import (
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestSortAndLimitErrorsIsGlobalFIFO(t *testing.T) {
	records := []contract.ErrorRecord{
		{ID: "b-new", RepoID: "b", TS: "2026-07-13T10:04:00Z"},
		{ID: "a-old", RepoID: "a", TS: "2026-07-13T10:01:00Z"},
		{ID: "b-old", RepoID: "b", TS: "2026-07-13T10:02:00Z"},
		{ID: "a-new", RepoID: "a", TS: "2026-07-13T10:03:00Z"},
	}

	got := sortAndLimitErrors(records, 3)
	want := []string{"b-old", "a-new", "b-new"}
	if len(got) != len(want) {
		t.Fatalf("records = %#v", got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("records[%d].ID = %q, want %q; all=%#v", i, got[i].ID, id, got)
		}
	}
}

func TestSortAndLimitErrorsBreaksTimestampTiesDeterministically(t *testing.T) {
	records := []contract.ErrorRecord{
		{ID: "z", RepoID: "b", TS: "2026-07-13T10:00:00Z"},
		{ID: "z", RepoID: "a", TS: "2026-07-13T10:00:00Z"},
		{ID: "a", RepoID: "a", TS: "2026-07-13T10:00:00Z"},
	}

	got := sortAndLimitErrors(records, 20)
	want := []string{"a/a", "a/z", "b/z"}
	for i, record := range got {
		if key := record.RepoID + "/" + record.ID; key != want[i] {
			t.Fatalf("records[%d] = %q, want %q", i, key, want[i])
		}
	}
}
