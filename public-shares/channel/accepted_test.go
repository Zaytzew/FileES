package channel

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func acceptedStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: t.TempDir(), Now: func() time.Time { return time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC) }}
}

// An empty shelf is an ordinary state, not an error: a channel may be created
// and never receive anything.
func TestListAcceptedOnAShelfThatNeverReceivedAnything(t *testing.T) {
	store := acceptedStore(t)
	entries, err := store.ListAccepted(uuid.NewString())
	if err != nil {
		t.Fatalf("empty shelf reported an error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries=%+v", entries)
	}
}

// The order arrivals are listed in is the order they arrived in, so a reader
// sees a chronology rather than whatever the filesystem happens to return.
func TestListAcceptedReturnsArrivalsOldestFirst(t *testing.T) {
	store := acceptedStore(t)
	channelID := uuid.NewString()
	when := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	for i, name := range []string{"trzeci.dwg", "pierwszy.dwg", "drugi.dwg"} {
		entry := Accepted{
			ChannelID: channelID, UploadID: uuid.NewString(), RepoPath: name, OriginalName: name,
			AcceptedAt: when.Add(time.Duration(i) * time.Hour),
		}
		if name == "pierwszy.dwg" {
			entry.AcceptedAt = when.Add(-time.Hour)
		}
		if err := store.RecordAccepted(entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.ListAccepted(channelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries=%+v", entries)
	}
	if entries[0].RepoPath != "pierwszy.dwg" || entries[1].RepoPath != "trzeci.dwg" {
		t.Fatalf("order=%q %q %q", entries[0].RepoPath, entries[1].RepoPath, entries[2].RepoPath)
	}
}

// A reaper that crashed between the commit and this write runs the same job
// again. The second record must replace the first, never add a second entry
// for one file.
func TestRecordAcceptedIsIdempotentByUploadID(t *testing.T) {
	store := acceptedStore(t)
	channelID, uploadID := uuid.NewString(), uuid.NewString()
	entry := Accepted{ChannelID: channelID, UploadID: uploadID, RepoPath: "rzut.dwg", OriginalName: "rzut.dwg", Revision: 7}
	if err := store.RecordAccepted(entry); err != nil {
		t.Fatal(err)
	}
	entry.Revision = 8
	if err := store.RecordAccepted(entry); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListAccepted(channelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Revision != 8 {
		t.Fatalf("entries=%+v", entries)
	}
}

// The record carries what a browser needs and stamps its own schema and time,
// so a caller cannot half-fill it.
func TestRecordAcceptedStampsSchemaAndTime(t *testing.T) {
	store := acceptedStore(t)
	channelID, uploadID := uuid.NewString(), uuid.NewString()
	if err := store.RecordAccepted(Accepted{ChannelID: channelID, UploadID: uploadID, RepoPath: "a.pdf"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := store.ListAccepted(channelID)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	if entries[0].Schema != AcceptedSchema {
		t.Fatalf("schema=%q", entries[0].Schema)
	}
	if !entries[0].AcceptedAt.Equal(time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("acceptedAt=%v", entries[0].AcceptedAt)
	}
}

// Identifiers reach this store from a daemon, but a path separator in one
// would write outside the shelf. Refuse rather than clean up silently.
func TestRecordAcceptedRefusesSeparatorsAndBlanks(t *testing.T) {
	store := acceptedStore(t)
	for _, entry := range []Accepted{
		{ChannelID: "", UploadID: uuid.NewString()},
		{ChannelID: uuid.NewString(), UploadID: " "},
		{ChannelID: "../elsewhere", UploadID: uuid.NewString()},
		{ChannelID: uuid.NewString(), UploadID: "a/b"},
	} {
		if err := store.RecordAccepted(entry); err == nil {
			t.Fatalf("accepted a record it should have refused: %+v", entry)
		}
	}
}

// Clearing a shelf drops what is left to handle. The repository keeps its
// history; these records do not describe history, they describe the queue.
func TestForgetAcceptedDropsOnlyTheNamedArrivals(t *testing.T) {
	store := acceptedStore(t)
	channelID := uuid.NewString()
	keep, drop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{keep, drop} {
		if err := store.RecordAccepted(Accepted{ChannelID: channelID, UploadID: id, RepoPath: id + ".dwg"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ForgetAccepted(channelID, []string{drop, uuid.NewString()}); err != nil {
		t.Fatalf("forgetting an absent arrival is not an error: %v", err)
	}
	entries, _ := store.ListAccepted(channelID)
	if len(entries) != 1 || entries[0].UploadID != keep {
		t.Fatalf("entries=%+v", entries)
	}
}

// One corrupt file must not hide the rest of the shelf: the owner still needs
// to see, and fetch, everything that is readable.
func TestListAcceptedSkipsAnUnreadableRecord(t *testing.T) {
	store := acceptedStore(t)
	channelID := uuid.NewString()
	good := uuid.NewString()
	if err := store.RecordAccepted(Accepted{ChannelID: channelID, UploadID: good, RepoPath: "dobry.dwg"}); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(store.acceptedDir(channelID), uuid.NewString()+".json")
	if err := os.WriteFile(corrupt, []byte("{nie-json"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListAccepted(channelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].UploadID != good {
		t.Fatalf("entries=%+v", entries)
	}
}
