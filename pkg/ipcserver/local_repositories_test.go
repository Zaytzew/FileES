package ipcserver

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

type localRepositoriesStub []contract.RepoSummary

func (s localRepositoriesStub) LocalRepositories() []contract.RepoSummary { return s }

func TestLocalRepositoryPresentationDoesNotCreateRuntimeOrAccess(t *testing.T) {
	s := New("unused")
	s.SetLocalRepositorySource(localRepositoriesStub{{ID: "local", ServerID: "office", LocalPath: "/local",
		Access: "rw", Attached: true, LocalCopy: &contract.LocalCopySummary{Path: "/local", Presence: "present", Kind: "virtual"}}})
	response := s.handleRepoList(contract.Request{RequestID: "list"})
	var list contract.RepoListResult
	if err := contract.DecodeResult(response.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Repos) != 1 || list.Repos[0].Attached || list.Repos[0].Access != "" || list.Repos[0].LocalCopy == nil {
		t.Fatalf("local fact grants authority: %+v", list)
	}
	if s.RepoState("office", "local") != nil {
		t.Fatal("presenter registered a syncing runtime")
	}
	statusResponse := s.handleRepoStatus(contract.Request{RequestID: "status", RepoID: "local"})
	var status contract.RepoStatus
	if err := contract.DecodeResult(statusResponse.Result, &status); err != nil {
		t.Fatal(err)
	}
	if status.Attached || status.Access != "" || status.State != contract.StateInitializing {
		t.Fatalf("status grants access: %+v", status)
	}
}

func TestLocalRepositoryOverlayPreservesAuthorityAndTerminalState(t *testing.T) {
	s := New("unused")
	s.SetLocalRepositorySource(localRepositoriesStub{{ID: "repo", ServerID: "office", LocalCopy: &contract.LocalCopySummary{Path: "/local", Presence: "present"}}})
	for _, row := range []contract.RepoSummary{
		{ID: "repo", ServerID: "office", Access: "r", State: "unattached"},
		{ID: "repo", ServerID: "office", ServerDeleted: true, State: "deleted"},
		{ID: "repo", ServerID: "office", LocalCopyPreserved: true},
		{ID: "repo", ServerID: "office", Attached: true, LocalPath: "/new"},
	} {
		out := s.mergeLocalRepositories([]contract.RepoSummary{row})
		if len(out) != 1 || out[0].Attached != row.Attached || out[0].Access != row.Access || out[0].State != row.State {
			t.Fatalf("authority overwritten: %+v", out)
		}
		if row.ServerDeleted || row.LocalCopyPreserved || row.LocalPath == "/new" {
			if out[0].LocalCopy != nil {
				t.Fatal("obsolete local fact overlaid terminal/moved row")
			}
		} else if out[0].LocalCopy == nil || out[0].LocalPath != "/local" {
			t.Fatal("known root not shown before runtime")
		}
	}
}
