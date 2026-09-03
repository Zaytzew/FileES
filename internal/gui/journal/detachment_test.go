package journal

import (
	"strings"
	"testing"
	"time"

	"filees/internal/gui/app"
)

func detachmentEntries(t *testing.T, vm app.ViewModel, now time.Time) []Entry {
	t.Helper()
	var found []Entry
	for _, entry := range BuildAt(vm, now) {
		if strings.HasPrefix(entry.ID, "detachment:") {
			found = append(found, entry)
		}
	}
	return found
}

func TestTheTwoDetachmentsGetDifferentSentences(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)
	vm := app.ViewModel{Detachments: []app.DetachmentViewModel{
		{ServerID: "manual", DisplayName: "manual", Cause: "self", At: "2026-09-03T17:40:00Z"},
		{ServerID: "spot", DisplayName: "spot", Cause: "revoked", At: "2026-09-03T18:10:00Z"},
	}}
	entries := detachmentEntries(t, vm, now)
	if len(entries) != 2 {
		t.Fatalf("detachment entries = %d, want 2", len(entries))
	}
	// Newest first, so the revoked one leads.
	revoked, self := entries[0], entries[1]
	if !strings.Contains(revoked.Summary, "odłączył tego klienta") {
		t.Errorf("revoked summary = %q; the server did this, not the owner", revoked.Summary)
	}
	if !strings.Contains(revoked.Details, "ponowna aktywacja") {
		t.Errorf("revoked details = %q; the cure is missing", revoked.Details)
	}
	if !strings.Contains(self.Summary, "Odłączono od serwera") {
		t.Errorf("self summary = %q", self.Summary)
	}
	// The opposite cure must never be offered for a decision already made and
	// finished. Suggesting re-activation here sends the owner to undo his own
	// three-times-confirmed choice.
	if strings.Contains(self.Details, "ponowna aktywacja") {
		t.Errorf("self details = %q; nothing is left to do about a deliberate detachment", self.Details)
	}
}

func TestADetachmentEntryCountsTheFoldersLeftBehind(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)
	vm := app.ViewModel{Detachments: []app.DetachmentViewModel{{
		ServerID: "manual", DisplayName: "manual", Cause: "self", At: "2026-09-03T17:40:00Z",
		WorkingCopies: []string{`C:\Projekty\Willa`, `C:\Projekty\Biurowiec`},
	}}}
	entries := detachmentEntries(t, vm, now)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if !strings.Contains(entries[0].Details, "2 foldery") {
		t.Errorf("details = %q; the files stayed on disk and the entry should say how many", entries[0].Details)
	}
}

func TestADetachmentSortsIntoTheChronologyByItsMoment(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)
	vm := app.ViewModel{
		Detachments: []app.DetachmentViewModel{
			{ServerID: "manual", DisplayName: "manual", Cause: "self", At: "2026-09-03T17:40:00Z"},
		},
		Notices: []app.NoticeViewModel{
			{ID: "n1", Title: "wcześniej", CreatedAt: "2026-09-03T10:00:00Z"},
			{ID: "n2", Title: "później", CreatedAt: "2026-09-03T19:00:00Z"},
		},
	}
	entries := BuildAt(vm, now)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	// A detachment is an event in the same chronology as everything else. If
	// it did not carry a moment it could only be pinned to one end or the
	// other, which is the whole reason a flag could not produce this entry.
	if entries[0].ID != "notice:n2" || entries[1].ID != "detachment:manual" || entries[2].ID != "notice:n1" {
		t.Fatalf("order = %s, %s, %s", entries[0].ID, entries[1].ID, entries[2].ID)
	}
}

// The chronology must not edit itself. The detachment happened; the owner
// re-activating the client afterwards does not un-happen it, and an entry that
// vanished because circumstances later changed would make the journal a
// summary of the present rather than a record of the past.
func TestAReattachedServerKeepsItsJournalEntry(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)
	vm := app.ViewModel{Detachments: []app.DetachmentViewModel{{
		ServerID: "manual", DisplayName: "manual", Cause: "revoked",
		At: "2026-09-03T18:10:00Z", ReattachedAt: "2026-09-03T19:30:00Z",
	}}}
	entries := detachmentEntries(t, vm, now)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: the detachment still happened", len(entries))
	}
	if entries[0].Timestamp != "2026-09-03T18:10:00Z" {
		t.Errorf("timestamp = %q; the entry keeps the moment it happened", entries[0].Timestamp)
	}
}

func TestADetachmentEntryNamesTheServerEvenWithoutADisplayName(t *testing.T) {
	now := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)
	vm := app.ViewModel{Detachments: []app.DetachmentViewModel{
		{ServerID: "atmprojekt:filees", Cause: "self", At: "2026-09-03T17:40:00Z"},
	}}
	entries := detachmentEntries(t, vm, now)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	// A12, arrived at from the other direction: the row a reader most needs to
	// identify must never render as a bare nothing.
	if !strings.Contains(entries[0].Summary, "atmprojekt:filees") {
		t.Errorf("summary = %q", entries[0].Summary)
	}
}
