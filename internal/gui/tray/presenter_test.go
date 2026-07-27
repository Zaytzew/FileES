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

func TestBuildMenuShowsUpdateBadgeAndCapabilityGatedUX(t *testing.T) {
	vm := app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapUpdatePlan: true, contract.CapUpdateApply: true},
		Update:       &app.UpdateViewModel{State: "available", CurrentVersion: "1.0", AvailableVersion: "1.1", ReleaseID: "r180", Summary: "Aktualizacja bezpieczeństwa"},
	}
	menu := BuildMenu(vm)
	update := findItem(t, menu.Items, "update")
	if update.Title != "● Dostępna aktualizacja 1.1" {
		t.Fatalf("update badge = %q", update.Title)
	}
	plan := findItem(t, update.Children, "update.plan")
	apply := findItem(t, update.Children, "update.apply")
	if plan.Intent == nil || plan.Intent.Kind != IntentUpdatePlan || apply.Intent == nil || apply.Intent.Kind != IntentUpdateApply {
		t.Fatalf("update intents = %#v / %#v", plan.Intent, apply.Intent)
	}

	vm.Stale = true
	stale := findItem(t, BuildMenu(vm).Items, "update")
	if hasItem(stale.Children, "update.plan") || hasItem(stale.Children, "update.apply") {
		t.Fatal("update mutations visible for stale status")
	}
}

func TestBuildMenuHidesUpdateWhenCurrent(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Update: &app.UpdateViewModel{State: "current", CurrentVersion: "1.1"}})
	if hasItem(menu.Items, "update") {
		t.Fatal("update badge visible without an available release")
	}
	if !hasItem(menu.Items, "action.update.placeholder") {
		t.Fatal("client update placeholder missing")
	}
}

func TestBuildMenuOffersDetachDeleteAndWholeStackLifecycle(t *testing.T) {
	repo := app.RepoViewModel{
		ID: "docs", ServerID: "office", DisplayName: "Dokumenty", Attached: true,
		AttachmentPolicy: "optional", OwnerRealmID: "realm", LocalPath: "/wc/docs",
		Access: contract.AccessReadWrite, State: contract.StateActive,
	}
	vm := app.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoDetach:     true,
			contract.CapRepoDelete:     true,
			contract.CapSystemRestart:  true,
			contract.CapSystemShutdown: true,
		},
		Repos: []app.RepoViewModel{repo},
		Servers: []app.ServerViewModel{{
			ID: "office", RealmID: "realm", CanCreateRepositories: true,
			ClientRole: contract.ClientRoleNormal, Repos: []app.RepoViewModel{repo},
		}},
	}
	menu := BuildMenu(vm)
	repoItem := findItem(t, menu.Items, "repo.docs")
	if !hasItem(repoItem.Children, "repo.docs.detach") || !hasItem(repoItem.Children, "repo.docs.delete") {
		t.Fatalf("repository lifecycle actions=%+v", repoItem.Children)
	}
	if !hasItem(menu.Items, "action.restart_filees") || !hasItem(menu.Items, "action.shutdown_filees") {
		t.Fatalf("whole-stack lifecycle actions missing: %+v", menu.Items)
	}
	if hasItem(menu.Items, "action.quit") {
		t.Fatal("misleading GUI-only quit action is still visible")
	}

	repo.AttachmentPolicy = "required"
	vm.Repos[0] = repo
	vm.Servers[0].Repos[0] = repo
	required := findItem(t, BuildMenu(vm).Items, "repo.docs")
	if hasItem(required.Children, "repo.docs.detach") || hasItem(required.Children, "repo.docs.delete") {
		t.Fatalf("required repository can be detached: %+v", required.Children)
	}
}

func TestBuildMenuShowsRepositoryActionsWhenCapabilitiesAllow(t *testing.T) {
	operation := "commit"
	vm := app.ViewModel{
		Connected: true,
		Icon:      app.IconBusy,
		Capabilities: map[string]bool{
			contract.CapRepoLock:   true,
			contract.CapRepoUnlock: true,
			contract.CapErrorList:  true,
		},
		Repos: []app.RepoViewModel{{
			ID: "projectA", Attached: true, Access: contract.AccessReadWrite, LocalPath: "/wc/projectA", State: contract.StateActive,
			Connectivity: contract.ConnOnline, LocalRev: 41, HeadRev: 42,
			Pending: contract.PendingStats{Added: 1, Modified: 2, Deleted: 3}, CurrentOp: &operation,
		}},
	}

	menu := BuildMenu(withServer(vm))
	repo := findItem(t, menu.Items, "repo.projectA")
	if repo.Title != "◷ projectA" {
		t.Fatalf("repo title = %q", repo.Title)
	}
	open := findItem(t, repo.Children, "repo.projectA.open")
	lock := findItem(t, repo.Children, "repo.projectA.lock")
	unlock := findItem(t, repo.Children, "repo.projectA.unlock")
	if repo.Intent != nil || open.Intent == nil || open.Intent.Kind != IntentOpenFolder || lock.Intent == nil || lock.Intent.Kind != IntentLock || unlock.Intent == nil || unlock.Intent.Kind != IntentUnlock || repo.Tooltip != "/wc/projectA" {
		t.Fatalf("repository actions = %#v", repo)
	}
	if !hasItem(menu.Items, "errors") {
		t.Fatal("errors menu missing despite error.list capability")
	}
}

func TestBuildMenuShowsGlobalRecentActivityBesideHistory(t *testing.T) {
	menu := BuildMenu(app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRepoActivity: true, contract.CapErrorList: true},
		Repos:        []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Activity:     []app.ActivityViewModel{{RepoID: "docs", Path: "raport.pdf", Stage: "published", Revision: 18}},
	})
	activity := findItem(t, menu.Items, "activity")
	if activity.Title != "Ostatnia aktywność" || len(activity.Children) != 1 || activity.Children[0].Title != "Dokumenty / raport.pdf — Opublikowano · r18" {
		t.Fatalf("activity=%+v", activity)
	}
	if len(activity.Children[0].Children) != 0 || activity.Children[0].Enabled {
		t.Fatalf("activity row should be informational: %+v", activity.Children[0])
	}
}

func TestActivityStageLabelDistinguishesKindForPublishedAndFailed(t *testing.T) {
	cases := []struct {
		kind, stage string
		revision    int64
		want        string
	}{
		{"added", "published", 3, "Dodano · r3"},
		{"modified", "published", 3, "Zaktualizowano · r3"},
		{"deleted", "published", 3, "Usunięto · r3"},
		{"renamed", "published", 3, "Zmieniono nazwę · r3"},
		{"", "published", 3, "Opublikowano · r3"},
		{"added", "failed", 0, "Nie udało się: dodanie"},
		{"modified", "failed", 0, "Nie udało się: aktualizacja"},
		{"deleted", "failed", 0, "Nie udało się: usunięcie"},
		{"renamed", "failed", 0, "Nie udało się: zmiana nazwy"},
	}
	for _, c := range cases {
		got := activityStageLabel(app.ActivityViewModel{Kind: c.kind, Stage: c.stage, Revision: c.revision})
		if got != c.want {
			t.Errorf("activityStageLabel(kind=%q, stage=%q) = %q, want %q", c.kind, c.stage, got, c.want)
		}
	}
}

func TestBuildMenuHidesCapabilityActionsAndErrors(t *testing.T) {
	menu := BuildMenu(withServer(app.ViewModel{
		Connected: true,
		Repos:     []app.RepoViewModel{{ID: "repo", Attached: true, Access: contract.AccessReadWrite, LocalPath: "/wc", State: contract.StateActive}},
	}))
	repo := findItem(t, menu.Items, "repo.repo")
	if hasItem(repo.Children, "repo.repo.lock") || hasItem(repo.Children, "repo.repo.unlock") {
		t.Fatal("lock actions visible without capabilities")
	}
	if hasItem(menu.Items, "errors") {
		t.Fatal("errors visible without error.list capability")
	}
}

func TestBuildMenuHidesMutationsWhileSnapshotStale(t *testing.T) {
	menu := BuildMenu(withServer(app.ViewModel{
		Connected: true,
		Stale:     true,
		Capabilities: map[string]bool{
			contract.CapRepoLock:   true,
			contract.CapRepoUnlock: true,
		},
		Repos: []app.RepoViewModel{{ID: "repo", Attached: true, Access: contract.AccessReadWrite, LocalPath: "/wc", State: contract.StateActive}},
	}))
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
	menu := BuildMenu(withServer(app.ViewModel{
		Connected: true,
		Repos:     []app.RepoViewModel{{ID: "future", Attached: true, LocalPath: "/wc", State: "future-state"}},
	}))
	if got := findItem(t, menu.Items, "repo.future").Title; got != "○ future" {
		t.Fatalf("title = %q", got)
	}
}

func TestBuildMenuGroupsReadOnlyRepoUnderActiveServer(t *testing.T) {
	repo := app.RepoViewModel{ID: "archive", ServerID: "office", Attached: true, Access: contract.AccessReadOnly, LocalPath: "/wc/archive", State: contract.StateActive}
	menu := BuildMenu(app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoLock: true}, Repos: []app.RepoViewModel{repo}, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "filees.example.net", ClientRole: contract.ClientRoleNormal, Repos: []app.RepoViewModel{repo}}}})
	server := findItem(t, menu.Items, "server.office")
	if server.Title != "filees.example.net" {
		t.Fatalf("server title=%q", server.Title)
	}
	repoMenu := findItem(t, server.Children, "repo.archive")
	if repoMenu.Intent != nil || len(repoMenu.Children) != 1 || repoMenu.Children[0].Intent == nil || repoMenu.Children[0].Intent.Kind != IntentOpenFolder {
		t.Fatalf("read-only repository row=%#v", repoMenu)
	}
}

func TestBuildMenuHeaderShowsAtLeastOneActiveServer(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "filees.example.net"}}})
	if menu.Title != "FileES — Połączono — Klient aktywowany" {
		t.Fatalf("menu title = %q", menu.Title)
	}
}

func TestBuildMenuDoesNotSynthesizeDefaultServer(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Repos: []app.RepoViewModel{{ID: "orphan"}}})
	if hasItem(menu.Items, "server.default") || !hasItem(menu.Items, "servers.empty") {
		t.Fatalf("menu synthesized a server: %+v", menu.Items)
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

func TestServerMenuExposesReservationsOnlyWhenAdvertised(t *testing.T) {
	base := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "office"}}}
	without := findItem(t, BuildMenu(base).Items, "server.office")
	if hasItem(without.Children, "server.office.reservations") {
		t.Fatal("reservation action shown without capability")
	}
	base.Capabilities = map[string]bool{contract.CapRepoReservationList: true}
	with := findItem(t, BuildMenu(base).Items, "server.office")
	item := findItem(t, with.Children, "server.office.reservations")
	if item.Intent == nil || item.Intent.Kind != IntentServerReservations || item.Intent.ServerID != "office" {
		t.Fatalf("reservation action = %+v", item)
	}
}

func TestBuildMenuShowsProjectedUnattachedRepositoryByDisplayName(t *testing.T) {
	repo := app.RepoViewModel{ID: "repo-uuid", DisplayName: "Dokumenty wspólne", ServerID: "office", OwnerRealmID: "foreign", AttachmentPolicy: "required", Access: contract.AccessReadOnly, State: contract.StateUnattached}
	menu := BuildMenu(app.ViewModel{Connected: true, Repos: []app.RepoViewModel{repo}, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "filees.example.net", RealmID: "mine", Repos: []app.RepoViewModel{repo}}}})
	item := findItem(t, menu.Items, "repo.repo-uuid")
	if item.Title != "○ Dokumenty wspólne" {
		t.Fatalf("title=%q", item.Title)
	}
	if item.Enabled || item.Intent != nil || len(item.Children) != 0 {
		t.Fatalf("unattached repository must be a compact disabled row: %#v", item)
	}
}

func TestBuildMenuHidesOptionalRepositoryWithoutLocalFolder(t *testing.T) {
	repo := app.RepoViewModel{ID: "remote", DisplayName: "Archiwum", ServerID: "office", AttachmentPolicy: "optional", State: contract.StateUnattached}
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{repo}}}})
	server := findItem(t, menu.Items, "server.office")
	if hasItem(server.Children, "repo.remote") {
		t.Fatal("optional repository without a local folder clutters the tray")
	}
	if !hasItem(server.Children, "server.office.empty") {
		t.Fatal("empty local-folder label missing")
	}
}

func TestServerPermissionsAreOnlyInInformationDialog(t *testing.T) {
	allowed := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "full", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}})
	allowedServer := findItem(t, allowed.Items, "server.full")
	if hasItem(allowedServer.Children, "server.full.creation") || hasItem(allowedServer.Children, "server.full.role") {
		t.Fatal("server permission metadata clutters tray menu")
	}
	readOnly := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "ro", ClientRole: contract.ClientRoleReadOnly, CanCreateRepositories: true}}})
	readOnlyServer := findItem(t, readOnly.Items, "server.ro")
	if hasItem(readOnlyServer.Children, "server.ro.creation") || hasItem(readOnlyServer.Children, "server.ro.role") {
		t.Fatal("read-only permission metadata clutters tray menu")
	}
	if !hasItem(allowedServer.Children, "server.full.info") || !hasItem(readOnlyServer.Children, "server.ro.info") {
		t.Fatal("server information action is missing")
	}
}

func TestServerCreateRepositoryActionIsPermissionAndFreshnessGated(t *testing.T) {
	server := app.ServerViewModel{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{server}})
	action := findItem(t, findItem(t, menu.Items, "server.office").Children, "server.office.create")
	if action.Intent == nil || action.Intent.Kind != IntentCreateRepository || action.Intent.ServerID != "office" {
		t.Fatalf("create action = %#v", action)
	}
	server.ClientRole = contract.ClientRoleReadOnly
	if hasItem(findItem(t, BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.create") {
		t.Fatal("create action visible for read-only client")
	}
	server.ClientRole = contract.ClientRoleNormal
	if hasItem(findItem(t, BuildMenu(app.ViewModel{Connected: true, Stale: true, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.create") {
		t.Fatal("create action visible for stale snapshot")
	}
}

func TestServerPairMobileDeviceActionIsFreshnessGatedNotRoleGated(t *testing.T) {
	server := app.ServerViewModel{ID: "office", ClientRole: contract.ClientRoleReadOnly, CanCreateRepositories: false}
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{server}})
	action := findItem(t, findItem(t, menu.Items, "server.office").Children, "server.office.pair_mobile")
	if action.Intent == nil || action.Intent.Kind != IntentPairMobileDevice || action.Intent.ServerID != "office" {
		t.Fatalf("pair mobile action = %#v", action)
	}
	if hasItem(findItem(t, BuildMenu(app.ViewModel{Connected: true, Stale: true, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.pair_mobile") {
		t.Fatal("pair mobile action visible for stale snapshot")
	}
	if hasItem(findItem(t, BuildMenu(app.ViewModel{Connected: false, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.pair_mobile") {
		t.Fatal("pair mobile action visible while disconnected")
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

func withServer(vm app.ViewModel) app.ViewModel {
	if len(vm.Servers) == 0 && len(vm.Repos) > 0 {
		vm.Servers = []app.ServerViewModel{{ID: "test", DisplayName: "Test", Repos: vm.Repos}}
	}
	return vm
}
