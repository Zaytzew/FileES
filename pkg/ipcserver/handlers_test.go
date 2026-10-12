package ipcserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestErrorListReadClosesHistoricalHoldOnlyAfterConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	old := `{"ts":"2026-10-12T15:14:30Z","scope":"commit:docs","code":"COMMIT-3100","key":"commit.recovery_held","severity":"WARN","hint":"REQUIRE_ACTION","msg":"held","details":"commit recovery HOLD: transaction=exact-id phase=done revision=154"}`
	closed := `{"ts":"2026-10-12T15:16:30Z","scope":"commit:docs","code":"COMMIT-3100","key":"commit.recovery_resolved","severity":"INFO","hint":"NONE","msg":"confirmed","details":"commit recovery confirmed: transaction=exact-id revision=154"}`
	if err := os.WriteFile(path, []byte(old+"\n"+closed+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows := readLastErrors(path, 20)
	first := parseErrLine(rows[0], "docs")
	if first == nil || first.ID != "2026-10-12T15:14:30Z:COMMIT-3100" || first.MessageKey != "commit.recovery_resolved" || first.Hint != "NONE" || first.Severity != "INFO" || !strings.Contains(first.Details, "phase=done") {
		t.Fatal("old journal still requests action or lost identity", first)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != old+"\n"+closed+"\n" {
		t.Fatal("append-only history rewritten")
	}
}

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
