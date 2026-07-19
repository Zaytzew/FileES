package main

import (
	"context"
	"testing"

	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
)

type projectionStop func()

func (stop projectionStop) Stop(context.Context) error { stop(); return nil }

type projectionStarter struct{ starts, stops int }

func (starter *projectionStarter) Start(context.Context, reposupervisor.Desired) (reposupervisor.Instance, error) {
	starter.starts++
	return projectionStop(func() { starter.stops++ }), nil
}

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

func TestReconcileProjectedViewChangesLiveAuthority(t *testing.T) {
	serverID := "office"
	key := reposupervisor.Key{ServerID: serverID, RepoID: "00000000-0000-0000-0000-000000000001"}
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess(key.RepoID, "svn+ssh://_filees-client@old.example/repo", t.TempDir(), serverID, contract.AccessReadWrite)
	runtimes := map[reposupervisor.Key]repoRuntime{key: {state: state}}
	starter := &projectionStarter{}
	supervisor, err := reposupervisor.New(starter, nil)
	if err != nil {
		t.Fatal(err)
	}
	view := clientview.View{Generation: 1, Repositories: []clientview.Repository{{RepoID: key.RepoID, DisplayName: "Repo", URL: "svn+ssh://_filees-client@new.example/repo", Access: "rw", State: "active"}}}
	server := ipcserver.New(t.TempDir() + "/projection.sock")
	if err := reconcileProjectedView(t.Context(), supervisor, server, serverID, view, runtimes); err != nil {
		t.Fatal(err)
	}
	view.Generation = 2
	view.Repositories[0].Access = "r"
	if err := reconcileProjectedView(t.Context(), supervisor, server, serverID, view, runtimes); err != nil {
		t.Fatal(err)
	}
	if starter.starts != 2 || starter.stops != 1 {
		t.Fatalf("starts=%d stops=%d", starter.starts, starter.stops)
	}
	summary := state.Summary()
	if summary.Access != "r" || summary.URL != view.Repositories[0].URL {
		t.Fatalf("summary=%+v", summary)
	}
	if err := supervisor.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
