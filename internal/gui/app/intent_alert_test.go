package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestIntentRequiredTrayAlert(t *testing.T) {
	r := RepoViewModel{Attached: true, State: contract.StateActive, Pending: contract.PendingStats{RenameUncertain: 1}}
	if repoIconState(r) != IconError {
		t.Fatal("uncertainty is not visible in tray")
	}
	if aggregateIcon(true, []RepoViewModel{r}, 1) != IconShout {
		t.Fatal("intent alert replaced shout priority")
	}
	r.Pending.RenameUncertain = 0
	if repoIconState(r) == IconError {
		t.Fatal("resolved uncertainty still alerts")
	}
}
