package app

import "testing"

// A detached server leaves the view, and its repositories leave with it.
//
// Detaching is deliberate - three confirmations - and afterwards nothing about
// it can be done from here, so keeping it on screen is not information. It is
// the name of a server somebody has just cut ties with, beside the names of its
// repositories, sitting where anyone can read them. The owner named the case:
// a server whose subject matter he would rather not have on display with people
// around. That makes it an exposure, and only absence fixes it.
func TestADetachedServerLeavesTheViewWithItsRepositories(t *testing.T) {
	servers := []ServerViewModel{
		{ID: "atmprojekt:filees", DisplayName: "atmprojekt:filees"},
		{ID: "manual", DisplayName: "manual", Detached: true},
		{ID: "spot", DisplayName: "spot"},
	}
	repos := []RepoViewModel{
		{ID: "a", ServerID: "atmprojekt:filees"},
		{ID: "b", ServerID: "manual"},
		{ID: "c", ServerID: "manual"},
		{ID: "d", ServerID: "spot"},
	}

	kept := withoutDetached(servers)
	if len(kept) != 2 || kept[0].ID != "atmprojekt:filees" || kept[1].ID != "spot" {
		t.Fatalf("servers = %+v", kept)
	}

	// The heading disappearing while the folders remain would expose exactly
	// what removing the server was meant to withhold, and break every count.
	remaining := reposOfListedServers(repos, kept)
	if len(remaining) != 2 {
		t.Fatalf("repos = %+v", remaining)
	}
	for _, repo := range remaining {
		if repo.ServerID == "manual" {
			t.Fatalf("a repository of a detached server survived: %+v", repo)
		}
	}
}

// A repository with no server at all is local bookkeeping and must not be
// swept away by a rule about servers.
func TestRepositoriesWithoutAServerAreKept(t *testing.T) {
	kept := reposOfListedServers(
		[]RepoViewModel{{ID: "orphan"}, {ID: "b", ServerID: "manual"}},
		[]ServerViewModel{{ID: "spot"}},
	)
	if len(kept) != 1 || kept[0].ID != "orphan" {
		t.Fatalf("kept = %+v", kept)
	}
}

// Nothing detached, nothing removed.
func TestAnOrdinaryViewIsUntouched(t *testing.T) {
	servers := []ServerViewModel{{ID: "spot"}, {ID: "manual"}}
	repos := []RepoViewModel{{ID: "a", ServerID: "spot"}, {ID: "b", ServerID: "manual"}}
	if len(withoutDetached(servers)) != 2 || len(reposOfListedServers(repos, servers)) != 2 {
		t.Fatal("a view with no detached server must pass through unchanged")
	}
}
