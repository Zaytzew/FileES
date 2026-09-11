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
	got := Build(vm, testTexts())
	if len(got) != 2 {
		t.Fatalf("entries=%#v", got)
	}
	if !got[0].Emphasized || !strings.Contains(got[0].Summary, "⚠ BŁĄD") || !strings.Contains(got[0].Details, "bad.txt") {
		t.Fatalf("merged error=%#v", got[0])
	}
	if got[1].Summary != "Dokumenty — publikacja: 2 · r7" || got[1].Details != "a.txt\nb.txt" {
		t.Fatalf("aggregate=%#v", got[1])
	}
	// The host says the number. The sentence whose noun has to agree with it
	// travels as data, because choosing the form is the renderer's business.
	message := got[1].SummaryMessage
	if message == nil || message.Key != "journal.queuePublished" {
		t.Fatalf("counted summary=%#v", got[1])
	}
	if message.Args["count"] != "2" || message.Args["revision"] != "7" || message.Args["repo"] != "Dokumenty" {
		t.Fatalf("counted arguments=%#v", message.Args)
	}
}

func TestJournalSeparatesReceivedFromPublishedAtSameRevision(t *testing.T) {
	vm := app.ViewModel{Activity: []app.ActivityViewModel{
		{RepoID: "repo", Path: "incoming-a", Stage: "received", Revision: 117},
		{RepoID: "repo", Path: "incoming-b", Stage: "received", Revision: 117},
		{RepoID: "repo", Path: "outgoing", Stage: "published", Revision: 117},
		{RepoID: "repo", Path: "clean", Stage: "reconciled"},
	}}
	entries := Build(vm, testTexts())
	if len(entries) != 3 {
		t.Fatalf("merged directions: %+v", entries)
	}
	want := map[string]bool{"repo — pobrano zmiany: 2 · r117": false, "repo / outgoing — opublikowano · r117": false, "repo / clean — uzgodniono stan (bez wysyłania)": false}
	for _, entry := range entries {
		if _, ok := want[entry.Summary]; !ok {
			t.Fatalf("unexpected: %+v", entry)
		}
		want[entry.Summary] = true
	}
	for text, seen := range want {
		if !seen {
			t.Fatalf("missing %s", text)
		}
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
	got := BuildAt(vm, now, testTexts())
	if len(got) != 2 {
		t.Fatalf("entries=%#v", got)
	}
	var connection Entry
	for _, entry := range got {
		if strings.HasPrefix(entry.ID, "connectivity:") {
			connection = entry
		}
	}
	if connection.ID == "" || connection.Emphasized || !strings.Contains(connection.Summary, "2") {
		t.Fatalf("connectivity entry=%#v", connection)
	}
	// Repeated interruptions are one incident with a count inside the
	// sentence, so the whole sentence is one key and a language may put the
	// number where it belongs rather than after a dot.
	if connection.SummaryMessage == nil || connection.SummaryMessage.Key != "journal.connectivityCounted" {
		t.Fatalf("counted connectivity=%#v", connection.SummaryMessage)
	}
	if connection.SummaryMessage.Args["count"] != "2" || connection.SummaryMessage.Args["repo"] != "Dokumenty" {
		t.Fatalf("counted arguments=%#v", connection.SummaryMessage.Args)
	}
}

func TestJournalTimestampPresentation(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.Local)
	tests := []struct {
		value string
		want  string
	}{
		{now.Add(-30 * time.Second).Format(time.RFC3339), "przed chwilą"},
		// "4 minutes ago" and "3 days ago" are gone on purpose. Saying either
		// needs a plural rule and a relative-time vocabulary per language, and
		// the renderer already has both — it recomputes every journal
		// timestamp with Intl and falls back to this string only for a
		// timestamp it cannot parse. So the host says the clock or the date,
		// never a number that would have to agree with a noun.
		{now.Add(-time.Minute).Format(time.RFC3339), "13:59"},
		{now.Add(-4 * time.Minute).Format(time.RFC3339), "13:56"},
		{now.Add(-20 * time.Minute).Format(time.RFC3339), "13:40"},
		{now.AddDate(0, 0, -1).Format(time.RFC3339), "wczoraj"},
		{now.AddDate(0, 0, -3).Format(time.RFC3339), now.AddDate(0, 0, -3).Format("02:01 15:04")},
	}
	for _, test := range tests {
		if got := RelativeTimestamp(test.value, now, testTexts()); got != test.want {
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
	entries := Build(vm, testTexts())
	if len(entries) != 1 || entries[0].Details != "audio/test.wav · 1.5 KiB" {
		t.Fatalf("entries=%#v", entries)
	}
}

func TestBuildAggregatesInFlightPerRepositoryAndStage(t *testing.T) {
	vm := app.ViewModel{Activity: []app.ActivityViewModel{
		{RepoID: "repo", Path: "a", Stage: "pending", UpdatedAt: "2026-08-10T12:00:00Z"},
		{RepoID: "repo", Path: "b", Stage: "pending", UpdatedAt: "2026-08-10T12:00:01Z"},
	}}
	got := Build(vm, testTexts())
	if len(got) != 1 || got[0].Summary != "repo — oczekujące zmiany: 2" {
		t.Fatalf("entries=%#v", got)
	}
}
