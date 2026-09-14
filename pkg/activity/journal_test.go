package activity

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedReceiptReplayPreservesTimeAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.json")
	j, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	receipt := Entry{RepoID: "docs", Path: "folder", Kind: Deleted, Stage: Published, Revision: 46}
	if err := j.Record(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	j.now = func() time.Time { return now }
	if err := j.Record(receipt); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) || !j.List()[0].UpdatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatal("replayed receipt changed the existing history")
	}
	receipt.Revision = 47
	if err := j.Record(receipt); err != nil {
		t.Fatal(err)
	}
	if got := j.List()[0]; got.Revision != 47 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("new commit did not advance the history: %+v", got)
	}
}

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

func TestIncomingAndReconciledPersistWithoutInventedPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.json")
	j, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{{RepoID: "repo", Path: "incoming", Kind: Added, Stage: Received, Revision: 117}, {RepoID: "repo", Path: "clean", Kind: Modified, Stage: Reconciled}} {
		if err := j.Record(entry); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 2 {
		t.Fatal("lost receipt")
	}
	if err := j.Record(Entry{RepoID: "repo", Path: "false", Kind: Added, Stage: Reconciled, Revision: 117}); err == nil {
		t.Fatal("reconciliation claimed a commit")
	}
}

func TestJournalIsGloballyBoundedAndNewestFirst(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "activity.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	for _, item := range []struct{ repo, path string }{{"a", "one"}, {"b", "two"}, {"c", "three"}} {
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

func TestCommitGroupsSurviveRetentionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.json")
	j, err := Open(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{2, 5, 22} {
		for i := 0; i < n; i++ {
			if err := j.Record(Entry{RepoID: "repo", Path: fmt.Sprintf("batch%d/file%d", n, i), Kind: Added, Stage: Published, Revision: int64(n)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	j, err = Open(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int64]int{}
	for _, e := range j.List() {
		counts[e.Revision]++
	}
	for _, n := range []int64{2, 5, 22} {
		if counts[n] != int(n) {
			t.Fatalf("groups after restart: %v", counts)
		}
	}
	if err := j.Record(Entry{RepoID: "other", Path: "next", Kind: Added, Stage: Published, Revision: 99}); err != nil {
		t.Fatal(err)
	}
	for _, e := range j.List() {
		if e.RepoID == "repo" && e.Revision == 2 {
			t.Fatal("oldest group not evicted whole")
		}
	}
	if len(j.List()) != 28 {
		t.Fatalf("remaining paths: %d", len(j.List()))
	}
}

func TestLimitGroupsKeepsInterleavedMembersAndSeparatesDirections(t *testing.T) {
	entries := []Entry{
		{RepoID: "a", Stage: Published, Revision: 7, Path: "one"},
		{RepoID: "b", Stage: Published, Revision: 7, Path: "other"},
		{RepoID: "a", Stage: Received, Revision: 7, Path: "incoming"},
		{RepoID: "a", Stage: Published, Revision: 7, Path: "two"},
	}
	got := LimitGroups(entries, 1)
	if len(got) != 2 || got[0].Path != "one" || got[1].Path != "two" {
		t.Fatalf("cut or mixed groups: %+v", got)
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
