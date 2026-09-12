package uploadworker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/avscan"
)

// An accepted upload leaves a record on the shelf. Without it the file is in
// the delivery repository and nothing can say so: no listing, no arrival
// signal, nothing for a browser to read.
func TestReapRecordsTheArrivalOnTheShelf(t *testing.T) {
	reaper, job, _ := fixture(t, avscan.Clean)
	reaper.Publisher.Run = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if strings.Contains(name, "svnlook") {
			return []byte("/\n"), nil
		}
		return []byte("r42 committed by acme at 2026-09-12T10:00:00.000000Z\n"), nil
	}
	summary, err := reaper.Reap(context.Background())
	if err != nil || summary.Accepted != 1 || summary.Unindexed != 0 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	entries, err := reaper.Channels.ListAccepted(job.ChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	got := entries[0]
	if got.UploadID != job.UploadID || got.OriginalName != job.OriginalName || got.SHA256 != job.SHA256 {
		t.Fatalf("record does not describe the upload: %+v", got)
	}
	// The stored path is the one the naming policy produced, because that is
	// what a selective fetch will ask the repository for.
	if got.RepoPath != "Opinia-Lodz.pdf" {
		t.Fatalf("repoPath=%q", got.RepoPath)
	}
	if got.Revision != 42 {
		t.Fatalf("revision=%d; svnmucc said r42", got.Revision)
	}
}

// svnmucc's confirmation line is a convenience, not part of the guarantee. An
// unreadable one costs the browser a revision number and nothing else.
func TestReapAcceptsEvenWhenTheRevisionCannotBeRead(t *testing.T) {
	reaper, job, _ := fixture(t, avscan.Clean)
	summary, err := reaper.Reap(context.Background())
	if err != nil || summary.Accepted != 1 || summary.Unindexed != 0 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	entries, _ := reaper.Channels.ListAccepted(job.ChannelID)
	if len(entries) != 1 || entries[0].Revision != 0 {
		t.Fatalf("entries=%+v", entries)
	}
}

// The commit is the point of no return. If the arrival cannot be recorded the
// file is still in the repository, so the job must leave intake: retrying
// would meet the collision check and fail every minute from then on. The
// operator hears about it through a separate count, not through a lie in
// either direction.
func TestReapKeepsTheFileWhenTheShelfIndexCannotBeWritten(t *testing.T) {
	reaper, job, _ := fixture(t, avscan.Clean)
	// A file where the index directory belongs: MkdirAll refuses, so the
	// record cannot be written while the channel record stays readable.
	blocker := filepath.Join(reaper.Channels.Root, "upload-accepted")
	if err := os.WriteFile(blocker, []byte("nie katalog"), 0600); err != nil {
		t.Fatal(err)
	}
	summary, err := reaper.Reap(context.Background())
	if err != nil {
		t.Fatalf("a failed index write must not fail the run: %v", err)
	}
	if summary.Accepted != 1 || summary.Unindexed != 1 || summary.Failed != 0 {
		t.Fatalf("summary=%+v; the upload was accepted and only its record is missing", summary)
	}
	if _, err := os.Stat(reaper.Intake.PayloadPath(job.UploadID)); !os.IsNotExist(err) {
		t.Fatal("the job stayed in intake and will collide with itself on the next run")
	}
}
