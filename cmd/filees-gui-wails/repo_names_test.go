package main

import (
	"path/filepath"
	"testing"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/reponames"
)

func renamingService(t *testing.T) (*GUIService, *reponames.Store) {
	t.Helper()
	store, err := reponames.Open(filepath.Join(t.TempDir(), "repo-names.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := &GUIService{snapshot: Snapshot{}, emitter: &recordingEmitter{}}
	service.attachRepoNames(store, nil)
	service.onChange(guiapp.ViewModel{
		Connected: true,
		Repos: []guiapp.RepoViewModel{
			{ID: "docs", ServerID: "office", DisplayName: "KRAŃCOWA-PŁOŃSK"},
			{ID: "docs", ServerID: "home", DisplayName: "Dom"},
		},
		PublicShares: []guiapp.PublicShareViewModel{{ChannelID: "c1", ServerID: "office", RepoID: "docs", RepoDisplayName: "KRAŃCOWA-PŁOŃSK", State: "active"}},
	})
	return service, store
}

func TestRenameShowsTheNameEverywhereButKeepsTheDaemonsModel(t *testing.T) {
	service, _ := renamingService(t)
	var renamed string
	service.onRepoRenamed = func(serverID, repoID, name string) { renamed = serverID + "/" + repoID + "=" + name }

	naming, err := service.RenameRepository("office", "docs", "  Umowy  ")
	if err != nil {
		t.Fatal(err)
	}
	if naming.Name != "Umowy" || naming.OwnName != "KRAŃCOWA-PŁOŃSK" || !naming.Custom || renamed != "office/docs=Umowy" {
		t.Fatalf("naming=%+v renamed=%q", naming, renamed)
	}
	snapshot := service.Snapshot()
	byServer := map[string]RepoProjection{}
	for _, repo := range snapshot.Repositories {
		byServer[repo.ServerID] = repo
	}
	if got := byServer["office"]; got.DisplayName != "Umowy" || got.OwnName != "KRAŃCOWA-PŁOŃSK" {
		t.Fatalf("office row = %+v", got)
	}
	if got := byServer["home"]; got.DisplayName != "Dom" || got.OwnName != "" {
		t.Fatalf("the same repo ID on another server must keep its name: %+v", got)
	}
	// Actions, tray and dialogs read the shown model.
	if repo, _ := projectedRepo(service.viewModel(), "docs"); repo.ServerID == "office" && repo.DisplayName != "Umowy" {
		t.Fatalf("actions model = %+v", repo)
	}
	if share := service.viewModel().PublicShares[0]; share.RepoDisplayName != "Umowy" {
		t.Fatalf("share row = %+v", share)
	}
	// The daemon's model is never rewritten, so bringing the name back works.
	service.mu.RLock()
	own := service.daemonView.Repos[0].DisplayName
	service.mu.RUnlock()
	if own != "KRAŃCOWA-PŁOŃSK" {
		t.Fatalf("daemon model changed to %q", own)
	}
}

func TestEmptyOrOwnNameBringsTheOwnNameBack(t *testing.T) {
	for _, reset := range []string{"", "  ", "KRAŃCOWA-PŁOŃSK"} {
		service, store := renamingService(t)
		if _, err := service.RenameRepository("office", "docs", "Umowy"); err != nil {
			t.Fatal(err)
		}
		naming, err := service.RenameRepository("office", "docs", reset)
		if err != nil || naming.Custom || naming.Name != "KRAŃCOWA-PŁOŃSK" {
			t.Fatalf("reset %q: naming=%+v err=%v", reset, naming, err)
		}
		if _, ok := store.Name("office", "docs"); ok {
			t.Fatalf("reset %q left a stored name", reset)
		}
		for _, repo := range service.Snapshot().Repositories {
			if repo.OwnName != "" {
				t.Fatalf("reset %q still marks %+v", reset, repo)
			}
		}
	}
}

func TestRenameRefusesUnknownRepositoriesAndBadNames(t *testing.T) {
	service, store := renamingService(t)
	if _, err := service.RenameRepository("office", "missing", "X"); err == nil {
		t.Fatal("a repository outside the view must not get a name")
	}
	if _, err := service.RenameRepository("office", "docs", "dwie\nlinie"); err != reponames.ErrInvalidName {
		t.Fatalf("multi-line name: %v", err)
	}
	if _, ok := store.Name("office", "docs"); ok {
		t.Fatal("refused name was stored")
	}
}
