package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestIntentResolutionProjectionFence(t *testing.T) {
	a := PendingAction{ID: "intent", RepoID: "docs", ServerID: "spot", ExpectedIntentsResolved: true}
	s := newAppState().startPendingAction(a).awaitPendingAction(a.ID)
	s = s.applySnapshot(contract.RepoStatus{RepoID: "docs", ServerID: "spot", Pending: contract.PendingStats{RenameUncertain: 1}})
	s, waiting := s.confirmPendingActions([]string{a.ID})
	if len(waiting) != 1 {
		t.Fatal("stale uncertainty crossed fence")
	}
	s = s.applySnapshot(contract.RepoStatus{RepoID: "docs", ServerID: "spot", Pending: contract.PendingStats{Added: 1, Deleted: 1}})
	s, waiting = s.confirmPendingActions([]string{a.ID})
	if len(waiting) != 0 || len(s.pendingActions) != 0 {
		t.Fatal("updated queue did not release spinner")
	}
}
