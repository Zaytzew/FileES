package journal

import (
	"strings"
	"testing"
	"time"

	"filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
)

func TestDurableReleaseStatesUseOneReceiptAndDoNotClaimPublication(t *testing.T) {
	for _, state := range []string{"queued", "running", "attached"} {
		vm := app.ViewModel{Repos: []app.RepoViewModel{{ID: "repo", DisplayName: "Project"}},
			AnchorReleaseQueue: []contract.RepoAnchorQueueResult{{IntentID: "confirmation", RepoID: "repo", UpdatedAt: "2026-10-10T10:00:00Z",
				RepoAnchorBatchResult: contract.RepoAnchorBatchResult{State: state, Path: "folder", Total: 3}}}}
		seen := ""
		texts := Texts{Chrome: func(key, fallback string) string { seen = key; return "Release state: %s" }}
		for range 2 {
			got := BuildAt(vm, time.Now(), texts)
			if len(got) != 1 || got[0].ID != "release-queue:confirmation" || got[0].Summary != "Release state: folder" ||
				!strings.HasPrefix(seen, "journal.releaseQueue.") || strings.Contains(got[0].Details, "r3") {
				t.Fatal(got, seen)
			}
		}
		vm.AnchorReleaseQueue[0].Superseded = true
		if got := Build(vm, texts); len(got) != 0 {
			t.Fatal("superseded outcome still visible", got)
		}
	}
}
