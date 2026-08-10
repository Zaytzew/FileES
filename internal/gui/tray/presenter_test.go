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
}

func TestBuildMenuClientGroupGatesRestartAndShutdown(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true})
	client := findItem(t, menu.Items, "client")
	if client.Title != "Klient" {
		t.Fatalf("client group title = %q", client.Title)
	}
	if !hasItem(client.Children, "action.reconnect") {
		t.Fatal("reconnect should always be present, it has no capability gate")
	}
	if hasItem(client.Children, "action.restart_filees") || hasItem(client.Children, "action.shutdown_filees") {
		t.Fatalf("restart/shutdown visible without capabilities: %+v", client.Children)
	}

	menu = BuildMenu(app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapSystemRestart: true, contract.CapSystemShutdown: true},
	})
	client = findItem(t, menu.Items, "client")
	if !hasItem(client.Children, "action.restart_filees") || !hasItem(client.Children, "action.shutdown_filees") {
		t.Fatalf("restart/shutdown missing with capabilities granted: %+v", client.Children)
	}
}

func TestBuildMenuDoesNotOfferRealmAliasForRealmWithRepositories(t *testing.T) {
	vm := app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRealmAliasClaim: true},
		Servers: []app.ServerViewModel{{
			ID: "manual", RealmID: "realm-acme",
			Repos: []app.RepoViewModel{{ID: "docs", ServerID: "manual", OwnerRealmID: "realm-acme", Attached: true, LocalPath: "/wc/docs"}},
		}},
	}
	server := findItem(t, BuildMenu(vm).Items, "server.manual")
	if hasItem(server.Children, "server.manual.realm_alias") {
		t.Fatalf("alias claim offered for established realm repositories: %+v", server.Children)
	}

	vm.Servers[0].Repos = nil
	server = findItem(t, BuildMenu(vm).Items, "server.manual")
	if !hasItem(server.Children, "server.manual.realm_alias") {
		t.Fatalf("alias recovery missing for a fresh empty realm: %+v", server.Children)
	}
}

func TestBuildMenuHeaderHasNoInfoRows(t *testing.T) {
	vm := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "Biuro"}}}
	menu := BuildMenu(vm)
	if hasItem(menu.Items, "system.status") || hasItem(menu.Items, "system.refreshed") {
		t.Fatalf("header info rows should not appear as menu items: %+v", menu.Items)
	}
	if menu.Title != "FileES — Połączono" || menu.Tooltip != "FileES — Połączono — repozytoria: 0" {
		t.Fatalf("header no longer carries activation suffix = %#v", menu)
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
	serverItem := findItem(t, menu.Items, "server.office")
	if hasItem(serverItem.Children, "repo.docs.detach") || hasItem(serverItem.Children, "repo.docs.delete") {
		t.Fatalf("folder lifecycle actions must not be duplicated at server level: %+v", serverItem.Children)
	}
	if hasItem(repoItem.Children, "repo.docs.detach") || hasItem(repoItem.Children, "repo.docs.delete") {
		t.Fatalf("folder lifecycle actions must be managed through settings: %+v", repoItem.Children)
	}
	client := findItem(t, menu.Items, "client")
	if !hasItem(client.Children, "action.restart_filees") || !hasItem(client.Children, "action.shutdown_filees") {
		t.Fatalf("whole-stack lifecycle actions missing from client group: %+v", client.Children)
	}
	if hasItem(menu.Items, "action.quit") {
		t.Fatal("misleading GUI-only quit action is still visible")
	}

	repo.AttachmentPolicy = "required"
	vm.Repos[0] = repo
	vm.Servers[0].Repos[0] = repo
	requiredRepo := findItem(t, findItem(t, BuildMenu(vm).Items, "server.office").Children, "repo.docs")
	if hasItem(requiredRepo.Children, "repo.docs.detach") || hasItem(requiredRepo.Children, "repo.docs.delete") {
		t.Fatalf("required repository can be detached: %+v", requiredRepo.Children)
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
			Connectivity: contract.ConnOnline, LocalRev: 41, HeadRev: 42, ReservationCount: 1,
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
	if !hasItem(menu.Items, "journal") {
		t.Fatal("journal menu missing despite error.list capability")
	}
}

func TestBuildMenuDisablesUnlockWithoutReservation(t *testing.T) {
	repo := app.RepoViewModel{ID: "projectA", Attached: true, Access: contract.AccessReadWrite, LocalPath: "/wc/projectA", State: contract.StateActive}
	menu := BuildMenu(withServer(app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoUnlock: true}, Repos: []app.RepoViewModel{repo}}))
	unlock := findItem(t, findItem(t, menu.Items, "repo.projectA").Children, "repo.projectA.unlock")
	if unlock.Enabled || unlock.Intent != nil {
		t.Fatalf("unlock without reservation must be disabled: %#v", unlock)
	}
}

func TestBuildMenuShowsCombinedJournalWithOpenAction(t *testing.T) {
	menu := BuildMenu(app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRepoActivity: true, contract.CapErrorList: true},
		Repos:        []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Activity:     []app.ActivityViewModel{{RepoID: "docs", Path: "raport.pdf", Stage: "published", Revision: 18, UpdatedAt: "2026-08-10T12:00:00Z"}},
		Errors:       []app.ErrorViewModel{{ID: "e1", RepoID: "docs", Timestamp: "2026-08-10T12:01:00Z", Severity: "ERROR", Code: "LOCK-1", Message: "Plik jest zablokowany", Hint: "REQUIRE_ACTION"}},
	})
	journal := findItem(t, menu.Items, "journal")
	if journal.Title != "Dziennik · ⚠ 1" || len(journal.Children) != 4 {
		t.Fatalf("journal=%+v", journal)
	}
	if journal.Children[0].Enabled || journal.Children[0].Title != "⚠ BŁĄD · Dokumenty — [LOCK-1] Plik jest zablokowany" || journal.Children[0].Tooltip != "Wymagane działanie użytkownika" {
		t.Fatalf("error row=%+v", journal.Children[0])
	}
	open := findItem(t, journal.Children, "journal.open")
	if open.Title != "Otwórz log…" || open.Intent == nil || open.Intent.Kind != IntentJournal || hasItem(menu.Items, "activity") || hasItem(menu.Items, "errors") {
		t.Fatalf("journal action or legacy menus are wrong: %+v", menu.Items)
	}
}

func TestBuildMenuLimitsJournalPreviewToTwelveEntries(t *testing.T) {
	activity := make([]app.ActivityViewModel, 13)
	for index := range activity {
		activity[index] = app.ActivityViewModel{RepoID: "docs", Path: "plik", Stage: "published", Revision: int64(index + 1), UpdatedAt: time.Date(2026, 8, 10, 12, index, 0, 0, time.UTC).Format(time.RFC3339)}
	}
	menu := BuildMenu(app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoActivity: true}, Activity: activity})
	journal := findItem(t, menu.Items, "journal")
	// 12 informational rows, one separator and the full-journal action.
	if len(journal.Children) != 14 || journal.Children[0].Title != "docs / plik — opublikowano · r13" || journal.Children[11].Title != "docs / plik — opublikowano · r2" {
		t.Fatalf("limited journal=%+v", journal.Children)
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
	if hasItem(menu.Items, "journal") {
		t.Fatal("journal visible without activity/error capability")
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

func TestBuildMenuHeaderDoesNotVaryWithActivation(t *testing.T) {
	inactive := BuildMenu(app.ViewModel{Connected: true})
	active := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "filees.example.net"}}})
	if inactive.Title != active.Title || inactive.Title != "FileES — Połączono" {
		t.Fatalf("header should not carry an activation suffix: inactive=%q active=%q", inactive.Title, active.Title)
	}
}

func TestServerMenuExposesServerScopedManagementEntryPoint(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office"}}})
	item := findItem(t, findItem(t, menu.Items, "server.office").Children, "server.office.settings")
	if item.Title != "Zarządzaj serwerem…" || item.Intent == nil || item.Intent.Kind != IntentSettings || item.Intent.ServerID != "office" {
		t.Fatalf("settings item = %#v", item)
	}
	if hasItem(menu.Items, "action.settings") {
		t.Fatal("global settings entry must not be rendered")
	}
}

func TestBuildMenuDoesNotSynthesizeDefaultServer(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Repos: []app.RepoViewModel{{ID: "orphan"}}})
	if hasItem(menu.Items, "server.default") || !hasItem(menu.Items, "servers.empty") {
		t.Fatalf("menu synthesized a server: %+v", menu.Items)
	}
}

func TestServerMenuUsesAliasWithOnlyServerScopedManagementAction(t *testing.T) {
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "office", Address: "filees.example.net:2222"}}})
	server := findItem(t, menu.Items, "server.office")
	if server.Title != "office" {
		t.Fatalf("server title = %q", server.Title)
	}
	if !hasItem(server.Children, "server.office.settings") || hasItem(server.Children, "server.office.info") || hasItem(server.Children, "server.office.create") || hasItem(server.Children, "server.office.detach") {
		t.Fatalf("server management action remains in tray: %+v", server.Children)
	}
}

func TestBuildMenuFluentReservationsSectionVanishesWhenEmpty(t *testing.T) {
	base := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", DisplayName: "office"}}}
	if hasItem(BuildMenu(base).Items, "action.reservations") {
		t.Fatal("reservation action shown without capability")
	}
	base.Capabilities = map[string]bool{contract.CapRepoReservationList: true}
	menu := BuildMenu(base)
	if hasItem(menu.Items, "action.reservations") || hasItem(menu.Items, "sep.fluent") {
		t.Fatalf("fluent section must vanish entirely, separator included, when nothing is active: %+v", menu.Items)
	}
	base.Servers[0].ReservationsKnown = true
	base.Servers[0].ReservationCount = 1
	menu = BuildMenu(base)
	if !hasItem(menu.Items, "sep.fluent") {
		t.Fatal("fluent separator missing once a shortcut is active")
	}
	item := findItem(t, menu.Items, "action.reservations")
	if item.Intent == nil || item.Intent.Kind != IntentReservations {
		t.Fatalf("reservation action = %+v", item)
	}
	server := findItem(t, menu.Items, "server.office")
	if hasItem(server.Children, "server.office.reservations") {
		t.Fatal("reservation action must not be nested under the server")
	}
}

func TestBuildMenuShowsRecoveryEntryOnlyWhenRecoveryIsAvailable(t *testing.T) {
	base := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office"}}}
	if hasItem(BuildMenu(base).Items, "action.recoveries") {
		t.Fatal("recovery entry shown without recovery records")
	}
	base.Recoveries = []app.RecoveryViewModel{{OperationID: "recovery-1", ServerName: "Biuro"}}
	item := findItem(t, BuildMenu(base).Items, "action.recoveries")
	if item.Intent == nil || item.Intent.Kind != IntentRecoveries {
		t.Fatalf("recovery entry = %#v", item)
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

func TestBuildMenuShowsPendingLocalRepositoryCreation(t *testing.T) {
	repo := app.RepoViewModel{ID: "import", DisplayName: "Biblia Audio KIDS", ServerID: "office", LocalPath: `F:\BIBLIA\Biblia Audio KIDS`, AttachmentPolicy: "optional", State: contract.StateInitializing}
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", Repos: []app.RepoViewModel{repo}}}})
	server := findItem(t, menu.Items, "server.office")
	item := findItem(t, server.Children, "repo.import")
	if item.Title != "◷ Biblia Audio KIDS" || item.Tooltip != repo.LocalPath || item.Enabled {
		t.Fatalf("pending local repository = %#v", item)
	}
	if hasItem(server.Children, "server.office.empty") {
		t.Fatal("pending local import is incorrectly presented as no local folders")
	}
}

func TestServerPermissionsAndManagementDoNotClutterTray(t *testing.T) {
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
	if hasItem(allowedServer.Children, "server.full.info") || hasItem(readOnlyServer.Children, "server.ro.info") {
		t.Fatal("server information action remains in tray")
	}
}

func TestServerShowsFolderCreationShortcutWhenEligible(t *testing.T) {
	server := app.ServerViewModel{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}
	menu := BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{server}})
	shortcut := findItem(t, findItem(t, menu.Items, "server.office").Children, "server.office.create_folder")
	if shortcut.Intent == nil || shortcut.Intent.Kind != IntentCreateRepository || shortcut.Intent.ServerID != "office" {
		t.Fatalf("folder-creation shortcut = %#v", shortcut)
	}
	if shortcut.Title != "Dodaj pierwszy folder do FileES…" {
		t.Fatalf("first-folder label = %q", shortcut.Title)
	}
	server.ClientRole = contract.ClientRoleReadOnly
	if hasItem(findItem(t, BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.create_folder") {
		t.Fatal("create action visible in tray for read-only client")
	}
	server.ClientRole = contract.ClientRoleNormal
	if hasItem(findItem(t, BuildMenu(app.ViewModel{Connected: true, Stale: true, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.create_folder") {
		t.Fatal("create action visible for stale snapshot")
	}
	// A local folder already exists: the shortcut must survive (this is the
	// only other way to reach folder creation is the Settings dialog, which
	// forces picking an existing folder row first) but relabel itself so it
	// no longer claims to be adding the "first" folder.
	server.Repos = []app.RepoViewModel{{ID: "docs", Attached: true, LocalPath: "/wc/docs"}}
	withFolder := findItem(t, findItem(t, BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.create_folder")
	if withFolder.Intent == nil || withFolder.Intent.Kind != IntentCreateRepository {
		t.Fatal("folder-creation shortcut disappears once a local folder exists")
	}
	if withFolder.Title != "Dodaj kolejny folder do FileES…" {
		t.Fatalf("additional-folder label = %q", withFolder.Title)
	}
	server.Repos = []app.RepoViewModel{{ID: "remote", AttachmentPolicy: "optional", State: contract.StateUnattached}}
	if !hasItem(findItem(t, BuildMenu(app.ViewModel{Connected: true, Servers: []app.ServerViewModel{server}}).Items, "server.office").Children, "server.office.create_folder") {
		t.Fatal("remote projection without a local folder hides the shortcut")
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
