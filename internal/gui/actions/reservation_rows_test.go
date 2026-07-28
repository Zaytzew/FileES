package actions

import (
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
