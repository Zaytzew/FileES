package app

import (
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
)

func TestPendingPassportUsesLiveJournalAndAttention(t *testing.T) {
	s := newAppState()
	s.connected = true
	s = s.applyRepoList([]contract.RepoSummary{{ID: "repo", ServerID: "server", DisplayName: "Dokumenty", Attached: true}})
	status := contract.RepoStatus{RepoID: "repo", ServerID: "server", Attached: true, State: contract.StateInteractionRequired, Connectivity: contract.ConnOnline, PassportIssues: []contract.PassportIssue{{ID: "pending", Path: "Łódź.dwg", Since: "2026-09-06T11:00:00Z", Code: string(errcat.CodePassportUncertain), Message: string(errcat.KeyPassportUncertain)}}}
	s = s.applySnapshot(status)
	vm := s.viewModel()
	if vm.Icon != IconError || len(vm.Errors) != 1 || vm.Errors[0].Code != "LOCK-2103" || !strings.Contains(vm.Errors[0].Message, "Łódź.dwg") || !strings.Contains(vm.Errors[0].Message, errcat.Polish(string(errcat.KeyPassportUncertain))) {
		t.Fatalf("presentation=%+v", vm)
	}
	status.PassportIssues = nil
	status.State = contract.StateActive
	s = s.applySnapshot(status)
	if vm := s.viewModel(); len(vm.Errors) != 0 || vm.Icon == IconError {
		t.Fatalf("resolved state remains red: %+v", vm)
	}
}
