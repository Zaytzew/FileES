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
	if _, err := os.Stat(filepath.Join(wc, ".filees", "state", inboxName)); err != nil {
		t.Fatal(err)
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
