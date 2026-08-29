package shout

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFormatParse(t *testing.T) {
	msg := Format("  Ważna paka  ")
	if msg != Marker+" Ważna paka" {
		t.Fatalf("Format = %q", msg)
	}
	comment, ok := Parse("  " + msg + "\n")
	if !ok || comment != "Ważna paka" {
		t.Fatalf("Parse = %q %v", comment, ok)
	}
	if _, ok := Parse("Auto-commit by FileES"); ok {
		t.Fatal("ordinary log parsed as shout")
	}
	if _, ok := Parse(Marker); ok {
		t.Fatal("marker-only parsed as shout")
	}
}

func TestValidateComment(t *testing.T) {
	if err := ValidateComment("  "); err != ErrEmptyComment {
		t.Fatalf("empty: %v", err)
	}
	if err := ValidateComment("ok"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateComment("line\nbreak"); err != ErrCommentHasControl {
		t.Fatalf("control: %v", err)
	}
}

func TestLastSeenAndInbox(t *testing.T) {
	wc := t.TempDir()
	if _, ok, err := LoadLastSeen(wc); err != nil || ok {
		t.Fatalf("missing last_seen: ok=%v err=%v", ok, err)
	}
	if err := SaveLastSeen(wc, 17); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadLastSeen(wc)
	if err != nil || !ok || got != 17 {
		t.Fatalf("last_seen=%d ok=%v err=%v", got, ok, err)
	}

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	added, err := Remember(wc, "repo-1", []LogEntry{
		{Revision: 18, Message: Format("wydanie A")},
		{Revision: 19, Message: "Auto-commit by FileES"},
		{Revision: 18, Message: Format("wydanie A")},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].ID != "shout:repo-1:18" || added[0].Title != "wydanie A" {
		t.Fatalf("added=%#v", added)
	}
	again, err := Remember(wc, "repo-1", []LogEntry{{Revision: 18, Message: Format("wydanie A")}}, now)
	if err != nil || len(again) != 0 {
		t.Fatalf("duplicate remember: %#v %v", again, err)
	}
	open, err := OpenNotices(wc)
	if err != nil || len(open) != 1 || open[0].Acked {
		t.Fatalf("open=%#v err=%v", open, err)
	}
	if err := Ack(wc, "shout:repo-1:18"); err != nil {
		t.Fatal(err)
	}
	open, err = OpenNotices(wc)
	if err != nil || len(open) != 0 {
		t.Fatalf("acked still open: %#v", open)
	}
	recent, err := RecentNotices(wc, 20)
	if err != nil || len(recent) != 1 || !recent[0].Acked || recent[0].Revision != 18 {
		t.Fatalf("recent history=%#v err=%v", recent, err)
	}
	if _, err := os.Stat(filepath.Join(wc, ".filees", "state", inboxName)); err != nil {
		t.Fatal(err)
	}
}

func TestRecentNoticesBoundsReadHistoryButKeepsEveryUnread(t *testing.T) {
	wc := t.TempDir()
	records := make([]Record, 0, 9)
	for i := 1; i <= 7; i++ {
		records = append(records, Record{ID: NoticeID("docs", int64(i)), RepoID: "docs", Revision: int64(i), Title: "read", CreatedAt: time.Date(2026, 8, 17, 12, i, 0, 0, time.UTC).Format(time.RFC3339), Acked: true})
	}
	records = append(records,
		Record{ID: NoticeID("docs", 8), RepoID: "docs", Revision: 8, Title: "unread-8", CreatedAt: "2026-08-17T13:08:00Z"},
		Record{ID: NoticeID("docs", 9), RepoID: "docs", Revision: 9, Title: "unread-9", CreatedAt: "2026-08-17T13:09:00Z"},
	)
	if err := SaveInbox(wc, records); err != nil {
		t.Fatal(err)
	}
	recent, err := RecentNotices(wc, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 5 {
		t.Fatalf("recent=%#v", recent)
	}
	if recent[0].Revision != 5 || recent[2].Revision != 7 || recent[3].Revision != 8 || recent[3].Acked || recent[4].Revision != 9 || recent[4].Acked {
		t.Fatalf("wrong bounded history=%#v", recent)
	}
	withoutHistory, err := RecentNotices(wc, 0)
	if err != nil || len(withoutHistory) != 2 || withoutHistory[0].Revision != 8 || withoutHistory[1].Revision != 9 {
		t.Fatalf("unread-only recent=%#v err=%v", withoutHistory, err)
	}
}

func TestPruneAcknowledgedNeverDropsUnread(t *testing.T) {
	inbox := []Record{
		{ID: "read-old", Acked: true},
		{ID: "unread-old"},
		{ID: "read-new", Acked: true},
		{ID: "unread-new"},
	}
	got := pruneAcknowledged(inbox, 1)
	if len(got) != 3 || got[0].ID != "unread-old" || got[1].ID != "read-new" || got[2].ID != "unread-new" {
		t.Fatalf("pruned=%#v", got)
	}
}

func TestAdvanceInitializesWithoutScanning(t *testing.T) {
	wc := t.TempDir()
	fetched := false
	added, err := Advance(wc, "repo", 9, func(from, to int64) ([]LogEntry, error) {
		fetched = true
		return []LogEntry{{Revision: 9, Message: Format("nie")}}, nil
	}, time.Now())
	if err != nil || fetched || len(added) != 0 {
		t.Fatalf("init scan added=%v fetched=%v err=%v", added, fetched, err)
	}
	added, err = Advance(wc, "repo", 11, func(from, to int64) ([]LogEntry, error) {
		if from != 10 || to != 11 {
			t.Fatalf("range %d:%d", from, to)
		}
		return []LogEntry{{Revision: 11, Message: Format("ok")}}, nil
	}, time.Now())
	if err != nil || len(added) != 1 || added[0].Revision != 11 {
		t.Fatalf("advance=%#v err=%v", added, err)
	}
}
