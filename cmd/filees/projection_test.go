package main

import (
	"testing"

	"filees/pkg/clientview"
	"filees/pkg/reposupervisor"
)

func TestAttachedProjectionUsesServerAuthorityAndSkipsUnattachedRepositories(t *testing.T) {
	serverID := "office"
	attached := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000001"}
	view := clientview.View{Repositories: []clientview.Repository{
		{RepoID: "00000000-0000-0000-0000-000000000002", DisplayName: "Not pinned", URL: "svn+ssh://_filees-client@example/two", Access: "r", State: "active"},
		{RepoID: attached.RepoID, DisplayName: "Documents", URL: "svn+ssh://_filees-client@example/one", Access: "rw", State: "disabled"},
	}}
	got := attachedProjection(serverID, view, map[reposupervisor.Key]repoRuntime{attached: {}})
	if len(got) != 1 {
		t.Fatalf("desired=%+v", got)
	}
	if got[0].Key != attached || got[0].Access != "rw" || got[0].State != "disabled" || got[0].URL != view.Repositories[1].URL || got[0].DisplayName != "Documents" {
		t.Fatalf("desired=%+v", got[0])
	}
}
