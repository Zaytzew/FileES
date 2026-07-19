package tray

import (
	"testing"
	"time"

	app "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
)

func TestBuildMenuDisconnectedMarksSnapshotStale(t *testing.T) {
	vm := app.ViewModel{
		Connected:   false,
		Stale:       true,
		Icon:        app.IconDisconnected,
		LastRefresh: time.Date(2026, 7, 13, 20, 30, 0, 0, time.Local),
		Repos:       []app.RepoViewModel{{ID: "projectA", Access: contract.AccessReadWrite, State: contract.StateActive}},
	}
	menu := BuildMenu(vm)
	if menu.Icon != app.IconDisconnected || menu.Title != "FileES — Brak połączenia" {
		t.Fatalf("menu header = %#v", menu)
	}
	refresh := findItem(t, menu.Items, "system.refreshed")
	if refresh.Enabled || refresh.Title != "Ostatnia aktualizacja: 20:30:00 (dane nieaktualne)" {
		t.Fatalf("refresh item = %#v", refresh)
	}
}

func TestBuildMenuRepoDetailsAndCapabilityGating(t *testing.T) {
	operation := "commit"
	vm := app.ViewModel{
		Connected: true,
		Icon:      app.IconBusy,
		Capabilities: map[string]bool{
			contract.CapRepoLock:  true,
			contract.CapErrorList: true,
		},
		Repos: []app.RepoViewModel{{
			ID: "projectA", Access: contract.AccessReadWrite, LocalPath: "/wc/projectA", State: contract.StateActive,
			Connectivity: contract.ConnOnline, LocalRev: 41, HeadRev: 42,
			Pending: contract.PendingStats{Added: 1, Modified: 2, Deleted: 3}, CurrentOp: &operation,
		}},
	}

	menu := BuildMenu(vm)
	repo := findItem(t, menu.Items, "repo.projectA")
	if repo.Title != "projectA — Praca w toku" {
		t.Fatalf("repo title = %q", repo.Title)
	}
	if got := findItem(t, repo.Children, "repo.projectA.pending").Title; got != "Oczekujące zmiany: 6" {
		t.Fatalf("pending title = %q", got)
	}
	lock := findItem(t, repo.Children, "repo.projectA.lock")
	if lock.Intent == nil || lock.Intent.Kind != IntentLock || lock.Intent.RepoID != "projectA" {
		t.Fatalf("lock item = %#v", lock)
	}
	if hasItem(repo.Children, "repo.projectA.unlock") {
		t.Fatal("unlock must be hidden without repo.unlock capability")
	}
	if !hasItem(menu.Items, "errors") {
		t.Fatal("errors menu missing despite error.list capability")
	}
}

func TestBuildMenuHidesCapabilityActionsAndErrors(t *testing.T) {
	menu := BuildMenu(app.ViewModel{
		Connected: true,
		Repos:     []app.RepoViewModel{{ID: "repo", Access: contract.AccessReadWrite, LocalPath: "/wc", State: contract.StateActive}},
	})
	repo := findItem(t, menu.Items, "repo.repo")
	if hasItem(repo.Children, "repo.repo.lock") || hasItem(repo.Children, "repo.repo.unlock") {
		t.Fatal("lock actions visible without capabilities")
	}
	if hasItem(menu.Items, "errors") {
		t.Fatal("errors visible without error.list capability")
	}
}

func TestBuildMenuHidesMutationsWhileSnapshotStale(t *testing.T) {
	menu := BuildMenu(app.ViewModel{
		Connected: true,
		Stale:     true,
		Capabilities: map[string]bool{
			contract.CapRepoLock:   true,
			contract.CapRepoUnlock: true,
		},
		Repos: []app.RepoViewModel{{ID: "repo", Access: contract.AccessReadWrite, LocalPath: "/wc", State: contract.StateActive}},
	})
	repo := findItem(t, menu.Items, "repo.repo")
	if hasItem(repo.Children, "repo.repo.lock") || hasItem(repo.Children, "repo.repo.unlock") {
		t.Fatal("mutation actions visible while snapshot is stale")
	}
}

func TestBuildMenuStructuredErrorsNewestFirst(t *testing.T) {
	menu := BuildMenu(app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapErrorList: true},
		Errors: []app.ErrorViewModel{
			{ID: "old", Severity: "WARN", Code: "NET-4007", Message: "Offline"},
			{ID: "new", Severity: "ERROR", Code: "LOCK-2001", Hint: "REQUIRE_ACTION", Message: "Locked"},
		},
	})
	errors := findItem(t, menu.Items, "errors")
	if len(errors.Children) != 2 || errors.Children[0].Title != "[ERROR] LOCK-2001 — Locked" {
		t.Fatalf("errors = %#v", errors.Children)
	}
	if errors.Children[0].Tooltip != "Wymagane działanie użytkownika" {
		t.Fatalf("error hint tooltip = %q", errors.Children[0].Tooltip)
	}
}

func TestBuildMenuUnknownStateHasSafeFallback(t *testing.T) {
	menu := BuildMenu(app.ViewModel{
		Connected: true,
		Repos:     []app.RepoViewModel{{ID: "future", LocalPath: "/wc", State: "future-state"}},
	})
	if got := findItem(t, menu.Items, "repo.future").Title; got != "future — Stan nieznany" {
		t.Fatalf("title = %q", got)
	}
}

func TestBuildMenuGroupsReadOnlyRepoUnderActiveServer(t *testing.T) {
	repo := app.RepoViewModel{ID: "archive", ServerID: "office", Access: contract.AccessReadOnly, LocalPath: "/wc/archive", State: contract.StateActive}
	menu := BuildMenu(app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoLock: true}, Repos: []app.RepoViewModel{repo}, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "filees.example.net", ClientRole: contract.ClientRoleNormal, Repos: []app.RepoViewModel{repo}}}})
	server := findItem(t, menu.Items, "server.office")
	if server.Title != "filees.example.net" {
		t.Fatalf("server title=%q", server.Title)
	}
	repoMenu := findItem(t, server.Children, "repo.archive")
	if got := findItem(t, repoMenu.Children, "repo.archive.access").Title; got != "Dostęp: tylko odczyt" {
		t.Fatalf("access=%q", got)
	}
	if hasItem(repoMenu.Children, "repo.archive.lock") {
		t.Fatal("read-only repo exposes lock")
	}
}

func TestBuildMenuHeaderShowsAtLeastOneActiveServer(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "filees.example.net"}}})
	if menu.Title != "FileES — Połączono — Klient aktywowany" {
		t.Fatalf("menu title = %q", menu.Title)
	}
}

func TestServerMenuUsesAliasAndExposesInformationDialog(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "office", Address: "filees.example.net:2222"}}})
	server := findItem(t, menu.Items, "server.office")
	if server.Title != "office" {
		t.Fatalf("server title = %q", server.Title)
	}
	info := findItem(t, server.Children, "server.office.info")
	if info.Intent == nil || info.Intent.Kind != IntentServerInfo || info.Intent.ServerID != "office" {
		t.Fatalf("server info action = %+v", info)
	}
}

func TestBuildMenuShowsProjectedUnattachedRepositoryByDisplayName(t *testing.T) {
	repo := app.RepoViewModel{ID: "repo-uuid", DisplayName: "Dokumenty wspólne", ServerID: "office", Access: contract.AccessReadOnly, State: contract.StateUnattached}
	menu := BuildMenu(app.ViewModel{Connected: true, Repos: []app.RepoViewModel{repo}, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "filees.example.net", Repos: []app.RepoViewModel{repo}}}})
	item := findItem(t, menu.Items, "repo.repo-uuid")
	if item.Title != "Dokumenty wspólne — Nieprzypięte lokalnie" {
		t.Fatalf("title=%q", item.Title)
	}
	if findItem(t, item.Children, "repo.repo-uuid.open").Enabled {
		t.Fatal("unattached repository exposes open-folder action")
	}
}

func findItem(t *testing.T, items []MenuItemModel, id string) MenuItemModel {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
		if len(item.Children) > 0 && hasItem(item.Children, id) {
			return findItem(t, item.Children, id)
		}
	}
	t.Fatalf("menu item %q not found", id)
	return MenuItemModel{}
}

func hasItem(items []MenuItemModel, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
