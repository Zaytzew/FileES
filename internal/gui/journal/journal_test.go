package journal

import (
	"strings"
	"testing"

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
