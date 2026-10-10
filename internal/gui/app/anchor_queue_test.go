package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
	"time"
)

func TestDurableReleaseFailureSurvivesGUIRefreshWithoutDuplicateOrStaleAlarm(t *testing.T) {
	failed := contract.RepoAnchorQueueResult{IntentID: "intent", RepoID: "repo", UpdatedAt: "2026-10-10T12:00:00Z", RepoAnchorBatchResult: contract.RepoAnchorBatchResult{State: "failed", Path: "folder", FailureCode: "HEAD-2025", FailureKey: "head.release_queue_interrupted"}}
	for _, superseded := range []bool{false, true} {
		failed.Superseded = superseded
		var s appState
		for range 2 {
			s = s.applyFullSnapshot(contract.SystemStatusResult{AnchorReleaseQueue: []contract.RepoAnchorQueueResult{failed}}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, time.Now())
		}
		if superseded {
			if len(s.errors) != 0 {
				t.Fatal(s.errors)
			}
			continue
		}
		if len(s.errors) != 1 || s.errors[0].ID != "release-queue:intent" || s.errors[0].Hint != "REQUIRE_ACTION" || s.errors[0].MessageDetail != "folder" {
			t.Fatal(s.errors)
		}
	}
}
