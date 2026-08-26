package journal

import (
	"strings"
	"testing"
	"time"

	"filees/internal/gui/app"
)

func TestBuildMergesErrorsAndAggregatesPublishedRevisionNewestFirst(t *testing.T) {
	vm := app.ViewModel{
		Repos: []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Activity: []app.ActivityViewModel{
			{RepoID: "docs", Path: "a.txt", Kind: "added", Stage: "published", Revision: 7, UpdatedAt: "2026-08-10T12:00:00Z"},
			{RepoID: "docs", Path: "b.txt", Kind: "modified", Stage: "published", Revision: 7, UpdatedAt: "2026-08-10T12:00:01Z"},
			{RepoID: "docs", Path: "bad.txt", Kind: "modified", Stage: "failed", ErrorID: "err-1", UpdatedAt: "2026-08-10T12:01:00Z"},
		},
		Errors: []app.ErrorViewModel{{ID: "err-1", RepoID: "docs", Timestamp: "2026-08-10T12:01:00Z", Code: "SVN-1", Severity: "ERROR", Message: "odmowa"}},
	}
	got := Build(vm)
	if len(got) != 2 {
		t.Fatalf("entries=%#v", got)
	}
	if !got[0].Emphasized || !strings.Contains(got[0].Summary, "⚠ BŁĄD") || !strings.Contains(got[0].Details, "bad.txt") {
		t.Fatalf("merged error=%#v", got[0])
	}
	if got[1].Summary != "Dokumenty — publikacja: 2 elementy · r7" || got[1].Details != "a.txt\nb.txt" {
		t.Fatalf("aggregate=%#v", got[1])
	}
}

func TestBuildCollapsesConnectivityNoiseWithoutTouchingOtherErrors(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.Local)
	vm := app.ViewModel{
		Repos: []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Errors: []app.ErrorViewModel{
			{ID: "net-1", RepoID: "docs", Timestamp: "2026-08-23T10:00:00Z", Code: "NET-4007", Severity: "WARN"},
			{ID: "net-2", RepoID: "docs", Timestamp: "2026-08-23T11:00:00Z", Code: "NET-4007", Severity: "WARN"},
			{ID: "svn", RepoID: "docs", Timestamp: "2026-08-23T12:00:00Z", Code: "SVN-1", Severity: "ERROR", Message: "odmowa"},
		},
	}
	got := BuildAt(vm, now)
	if len(got) != 2 {
		t.Fatalf("entries=%#v", got)
	}
	var connection Entry
	for _, entry := range got {
		if strings.HasPrefix(entry.ID, "connectivity:") {
			connection = entry
		}
	}
	if connection.ID == "" || connection.Emphasized || !strings.Contains(connection.Summary, "2 zdarzenia") {
		t.Fatalf("connectivity entry=%#v", connection)
	}
}

func TestJournalTimestampPresentation(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.Local)
	tests := []struct {
		value string
		want  string
	}{
		{now.Add(-30 * time.Second).Format(time.RFC3339), "przed chwilą"},
		{now.Add(-time.Minute).Format(time.RFC3339), "minutę temu"},
		{now.Add(-4 * time.Minute).Format(time.RFC3339), "4 minuty temu"},
		{now.Add(-9 * time.Minute).Format(time.RFC3339), "9 minut temu"},
		{now.Add(-20 * time.Minute).Format(time.RFC3339), "13:40"},
		{now.AddDate(0, 0, -1).Format(time.RFC3339), "wczoraj"},
		{now.AddDate(0, 0, -3).Format(time.RFC3339), "3 dni temu"},
	}
	for _, test := range tests {
		if got := RelativeTimestamp(test.value, now); got != test.want {
			t.Errorf("RelativeTimestamp(%q)=%q, want %q", test.value, got, test.want)
		}
	}
	if got := ExactTimestamp("2026-08-23T12:34:56Z"); got != time.Date(2026, 8, 23, 12, 34, 56, 0, time.UTC).Local().Format("02:01:2006 15:04") {
		t.Fatalf("ExactTimestamp=%q", got)
	}
}

func TestJournalDetailsIncludeKnownObjectSizes(t *testing.T) {
	size := int64(1536)
	vm := app.ViewModel{Activity: []app.ActivityViewModel{{RepoID: "repo", Path: "audio/test.wav", Kind: "added", Stage: "published", Revision: 8, UpdatedAt: "2026-08-26T07:44:56Z", Size: &size}}}
	entries := Build(vm)
	if len(entries) != 1 || entries[0].Details != "audio/test.wav · 1.5 KiB" {
		t.Fatalf("entries=%#v", entries)
	}
}

func TestBuildAggregatesInFlightPerRepositoryAndStage(t *testing.T) {
	vm := app.ViewModel{Activity: []app.ActivityViewModel{
		{RepoID: "repo", Path: "a", Stage: "pending", UpdatedAt: "2026-08-10T12:00:00Z"},
		{RepoID: "repo", Path: "b", Stage: "pending", UpdatedAt: "2026-08-10T12:00:01Z"},
	}}
	got := Build(vm)
	if len(got) != 1 || got[0].Summary != "repo — oczekujące zmiany: 2" {
		t.Fatalf("entries=%#v", got)
	}
}
