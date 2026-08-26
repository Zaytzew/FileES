package activity

import (
	"path/filepath"
	"testing"
	"time"
)

func TestJournalSurvivesRestartAndCollapsesPipelineStages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.json")
	j, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	size := int64(1536)
	if err := j.Record(Entry{RepoID: "docs", Path: "report.pdf", Kind: Added, Stage: Detected, Size: &size}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := j.Record(Entry{RepoID: "docs", Path: "report.pdf", Stage: Published, Revision: 18}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	entries := reopened.List()
	if len(entries) != 1 || entries[0].Kind != Added || entries[0].Stage != Published || entries[0].Revision != 18 || !entries[0].DetectedAt.Equal(now.Add(-time.Minute)) || entries[0].Size == nil || *entries[0].Size != size {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestJournalIsGloballyBoundedAndNewestFirst(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "activity.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	for _, item := range []struct{ repo, path string }{{"a", "one"}, {"b", "two"}, {"a", "three"}} {
		if err := j.Record(Entry{RepoID: item.repo, Path: item.path, Kind: Modified, Stage: Pending}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	entries := j.List()
	if len(entries) != 2 || entries[0].Path != "three" || entries[1].Path != "two" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestJournalForgetRemovesEntryAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.json")
	j, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Record(Entry{RepoID: "docs", Path: "lockfile.tmp", Kind: Added, Stage: Pending}); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(Entry{RepoID: "docs", Path: "kept.txt", Kind: Added, Stage: Pending}); err != nil {
		t.Fatal(err)
	}
	if err := j.Forget("docs", "lockfile.tmp"); err != nil {
		t.Fatal(err)
	}
	entries := j.List()
	if len(entries) != 1 || entries[0].Path != "kept.txt" {
		t.Fatalf("entries=%+v, want only kept.txt", entries)
	}
	// Forgetting an unknown path is a harmless no-op, not an error.
	if err := j.Forget("docs", "never-existed"); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if entries := reopened.List(); len(entries) != 1 || entries[0].Path != "kept.txt" {
		t.Fatalf("entries after reopen=%+v, want only kept.txt", entries)
	}
}

func TestJournalRejectsFalsePublishedAndEscapingPath(t *testing.T) {
	j, _ := Open(filepath.Join(t.TempDir(), "activity.json"), 20)
	if err := j.Record(Entry{RepoID: "docs", Path: "../secret", Kind: Added, Stage: Detected}); err == nil {
		t.Fatal("escaping path accepted")
	}
	if err := j.Record(Entry{RepoID: "docs", Path: "file", Kind: Added, Stage: Published}); err == nil {
		t.Fatal("published without revision accepted")
	}
}
