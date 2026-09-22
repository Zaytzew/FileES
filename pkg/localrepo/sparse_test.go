package localrepo

import (
	"path/filepath"
	"testing"
)

// A working copy started from the unattached browser stays sparse across a
// restart - no resume may turn it into a full checkout - until the user asks
// for the whole folder.
func TestSparseAttachmentSurvivesRestartUntilFilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "Docs")
	if _, err := store.BeginSparseAttach("primary", "repo-1", local, "", false); err == nil {
		t.Fatal("a sparse attachment without its first path was accepted")
	}
	record, err := store.BeginSparseAttach("primary", "repo-1", local, "/art/chosen/", false)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Sparse || record.SparsePath != "art/chosen" {
		t.Fatalf("record = %+v", record)
	}
	if _, err := store.ApproveAttach(record.OperationID, "primary", "repo-1", "svn+ssh://_filees-data@example/repo-1", "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAttached(record.OperationID, "repo-1"); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, ok := reopened.Get(record.OperationID)
	if !ok || !again.Sparse || again.SparsePath != "art/chosen" {
		t.Fatalf("after restart = %+v ok=%v", again, ok)
	}
	if _, err := reopened.MarkFullDepth("primary", "repo-1"); err != nil {
		t.Fatal(err)
	}
	full, _ := reopened.Get(record.OperationID)
	if full.Sparse || full.SparsePath != "" {
		t.Fatalf("whole folder still recorded as sparse: %+v", full)
	}
}
