package commit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/activity"
)

type incomingJournal struct{ entries []activity.Entry }

func (j *incomingJournal) Record(entry activity.Entry) error {
	j.entries = append(j.entries, entry)
	return nil
}

type fixedRevision int64

func (r fixedRevision) Revision(context.Context, string) (int64, error) { return int64(r), nil }

// Owner, 2026-09-28: background updates of read-only attachments reached no
// journal; only his own commits did.
func TestRecordIncomingJournalsWhatAReadOnlyUpdateBroughtIn(t *testing.T) {
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "photos", "new.jpg"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := "Updating '.':\nA    photos/new.jpg\nU    notes.txt\nD    old.pdf\nC    conflicted.dwg\nUpdated to revision 12.\n"
	journal := &incomingJournal{}
	if !RecordIncoming(context.Background(), journal, fixedRevision(12), "repo-r", wc, output, nil) {
		t.Fatal("nothing recorded")
	}
	got := map[string]activity.Entry{}
	for _, entry := range journal.entries {
		got[entry.Path] = entry
		if entry.Stage != activity.Received || entry.Revision != 12 || entry.RepoID != "repo-r" {
			t.Fatalf("entry = %+v", entry)
		}
	}
	if len(got) != 3 || got["photos/new.jpg"].Kind != activity.Added || got["notes.txt"].Kind != activity.Modified || got["old.pdf"].Kind != activity.Deleted {
		t.Fatalf("entries = %+v (a conflict is not a receipt)", got)
	}
	if size := got["photos/new.jpg"].Size; size == nil || *size != 5 {
		t.Fatalf("size = %v", size)
	}
	if RecordIncoming(context.Background(), journal, fixedRevision(12), "repo-r", wc, "At revision 12.\n", nil) {
		t.Fatal("an update with no changes recorded something")
	}
}
