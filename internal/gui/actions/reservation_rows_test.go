package actions

import (
	"strings"
	"testing"
	"time"

	"filees/internal/gui/app"
)

func TestReservationRowsFormatTimeAndOfferFutureRequestForForeignLock(t *testing.T) {
	createdAt := "2026-07-28T15:04:05Z"
	entries := []reservationEntry{
		{serverName: "Serwer", workingCopyAlias: "Dokumenty", reservation: app.Reservation{WorkingCopy: "/work/docs", Path: "plan.pdf", OwnerLabel: "anna", CreatedAt: createdAt, CanRelease: true}},
		{serverName: "Serwer", workingCopyAlias: "Dokumenty", reservation: app.Reservation{WorkingCopy: "/work/docs", Path: "cudzy.pdf", OwnerLabel: "bartek", CreatedAt: createdAt, CanRelease: false}},
	}
	rows, _ := reservationRows(entries)
	if got, want := rows[0].CreatedAt, mustParseReservationTime(t, createdAt); got != want {
		t.Fatalf("formatted time = %q, want %q", got, want)
	}
	if got := rows[0].Action; got != "Zwolnij" {
		t.Fatalf("own reservation action = %q", got)
	}
	if got := rows[1].Action; got != "Poproś o zwolnienie (wkrótce)" {
		t.Fatalf("foreign reservation action = %q", got)
	}
}

func mustParseReservationTime(t *testing.T, value string) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Local().Format("15:04 02-01-2006")
}

// The reason a file cannot be edited has to survive all the way to the
// sentence a user reads. It used to die at the presentation boundary: the
// daemon knew who held the file and until when, sent both over IPC, and the
// GUI printed "the daemon did not perform the operation".
func TestHeldByOtherSentenceNamesTheHolderAndTheTime(t *testing.T) {
	sentence := detailedMessageLabel("lock.held_by_other", map[string]string{
		"path":   "rysunek.dwg",
		"holder": "anna",
		"until":  "2026-08-11T13:41:16Z",
	})
	for _, want := range []string{"rysunek.dwg", "anna"} {
		if !strings.Contains(sentence, want) {
			t.Fatalf("sentence %q does not mention %q", sentence, want)
		}
	}
	// Rendered in the reader's own timezone, since "until 13:41" is only
	// actionable if it means 13:41 on their clock.
	local, err := time.Parse(time.RFC3339, "2026-08-11T13:41:16Z")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sentence, local.Local().Format("15:04")) {
		t.Fatalf("sentence %q does not carry the local expiry", sentence)
	}
}

// A raw SVN lock carries no passport metadata, and every client authenticates
// as the same account, so there is genuinely nobody to name. Saying "somebody
// else" is honest; inventing an owner would not be.
func TestHeldByOtherSentenceAdmitsWhenTheHolderIsUnknown(t *testing.T) {
	sentence := detailedMessageLabel("lock.held_by_other", map[string]string{"path": "rysunek.dwg"})
	if !strings.Contains(sentence, "kogoś innego") {
		t.Fatalf("sentence %q does not admit the holder is unknown", sentence)
	}
	if strings.Contains(sentence, " do ") {
		t.Fatalf("sentence %q invented an expiry it was not given", sentence)
	}
}

// Other keys must not be dressed up by this path; they keep their plain
// label, so an unrelated failure cannot accidentally render as a lock notice.
func TestDetailedMessageLabelIgnoresUnrelatedKeys(t *testing.T) {
	if got := detailedMessageLabel("lock.operation_failed", map[string]string{"detail": "svn: E160039"}); got != "" {
		t.Fatalf("unrelated key produced a sentence: %q", got)
	}
}
