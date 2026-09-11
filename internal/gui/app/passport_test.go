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
	// This layer projects state and must not author the sentence: it carries
	// the key the daemon sent and the instance it applies to, and the
	// composition renders both where the catalogue lives.
	if vm.Icon != IconError || len(vm.Errors) != 1 {
		t.Fatalf("presentation=%+v", vm)
	}
	issue := vm.Errors[0]
	if issue.Code != "LOCK-2103" || issue.MessageKey != string(errcat.KeyPassportUncertain) {
		t.Fatalf("issue lost its identity: %+v", issue)
	}
	if issue.MessageDetail != "Łódź.dwg" {
		t.Fatalf("issue lost the path it applies to: %+v", issue)
	}
	if strings.Contains(issue.Message, "rezerwacj") {
		t.Fatalf("the projection authored a sentence: %+v", issue)
	}
	status.PassportIssues = nil
	status.State = contract.StateActive
	s = s.applySnapshot(status)
	if vm := s.viewModel(); len(vm.Errors) != 0 || vm.Icon == IconError {
		t.Fatalf("resolved state remains red: %+v", vm)
	}
}
