package tray

import (
	"fmt"
	"strings"

	app "filees/internal/gui/app"
	"filees/internal/gui/journal"
)

// BuildMenu converts an app ViewModel into a deterministic tray menu.
func BuildMenu(vm app.ViewModel) MenuModel {
	status := connectionLabel(vm)
	model := MenuModel{
		Icon:    vm.Icon,
		Title:   "FileES — " + status,
		Tooltip: fmt.Sprintf("FileES — %s — repozytoria: %d", status, len(vm.Repos)),
	}
	model.Items = append(model.Items, fluentItems(vm)...)
	if vm.Update.Available() {
		model.Items = append(model.Items, updateMenu(vm))
	}
	model.Items = append(model.Items, separator("sep.repositories"))

	if len(vm.Servers) == 0 {
		model.Items = append(model.Items, disabledItem("servers.empty", "Brak aktywnych serwerów"))
	} else {
		for _, server := range vm.Servers {
			model.Items = append(model.Items, serverMenu(vm, server))
		}
	}

	if vm.CanListActivity() || vm.CanListErrors() {
		model.Items = append(model.Items, separator("sep.history"), journalMenu(vm))
	}
	if len(vm.Recoveries) > 0 {
		model.Items = append(model.Items, actionItem("action.recoveries", "Odzyskiwanie repozytoriów…", "Pobierz dostępne archiwa odzyskiwania", Intent{Kind: IntentRecoveries}))
	}
	model.Items = append(model.Items,
		separator("sep.actions"),
		actionItem("action.activate", "Aktywuj klienta na nowym serwerze…", "Dodaj aktywację FileES kodem z e-maila", Intent{Kind: IntentActivate}),
	)
	if !vm.Update.Available() {
		model.Items = append(model.Items, disabledItem("action.update.placeholder", "Aktualizacja klienta — w przygotowaniu"))
	}
	model.Items = append(model.Items, clientMenu(vm))
	return model
}

func journalMenu(vm app.ViewModel) MenuItemModel {
	entries := journal.Build(vm)
	visible := entries
	if len(visible) > journal.TrayLimit {
		visible = visible[:journal.TrayLimit]
	}
	children := make([]MenuItemModel, 0, len(visible)+2)
	for index, entry := range visible {
		item := disabledItem(fmt.Sprintf("journal.%d.%s", index, entry.ID), entry.Summary)
		item.Tooltip = entry.Details
		children = append(children, item)
	}
	if len(children) == 0 {
		children = append(children, disabledItem("journal.empty", "Brak wpisów"))
	}
	children = append(children,
		separator("journal.sep.open"),
		actionItem("journal.open", "Otwórz log…", "Pokaż pełny dziennik aktywności i błędów", Intent{Kind: IntentJournal}),
	)
	title := "Dziennik"
	errorCount := 0
	for _, entry := range entries {
		if entry.Emphasized {
			errorCount++
		}
	}
	if errorCount > 0 {
		title = fmt.Sprintf("Dziennik · ⚠ %d", errorCount)
	}
	return MenuItemModel{ID: "journal", Title: title, Enabled: true, Children: children}
}

// fluentItems returns menu items for transient, state-driven shortcuts.
// The whole section -- including its separator -- is omitted entirely when
// nothing is currently active, never left behind as a disabled placeholder.
func fluentItems(vm app.ViewModel) []MenuItemModel {
	var items []MenuItemModel
	if vm.SupportsReservationListing() && vm.CanBrowseReservations() {
		items = append(items, actionItem("action.reservations", "Lista rezerwacji plikowych", "Pokaż aktywne rezerwacje ze wszystkich serwerów FileES", Intent{Kind: IntentReservations}))
	}
	if len(items) == 0 {
		return nil
	}
	return append([]MenuItemModel{separator("sep.fluent")}, items...)
}

// clientMenu groups the GUI<->local-daemon lifecycle actions. These are
// local, technical operations unrelated to any daemon-server connection;
// nesting them keeps the list of such operations free to grow without
// crowding the flat top level of the menu.
func clientMenu(vm app.ViewModel) MenuItemModel {
	children := []MenuItemModel{
		actionItem("action.reconnect", "Połącz ponownie", "Odśwież połączenie z daemonem", Intent{Kind: IntentReconnect}),
	}
	if vm.CanRestartFileES() {
		children = append(children, actionItem("action.restart_filees", "Uruchom FileES ponownie…", "Kontrolowanie zrestartuj daemon i GUI", Intent{Kind: IntentRestartFileES}))
	}
	if vm.CanShutdownFileES() {
		children = append(children, actionItem("action.shutdown_filees", "Zamknij FileES…", "Kontrolowanie zatrzymaj synchronizację, daemon i GUI", Intent{Kind: IntentShutdownFileES}))
	}
	return MenuItemModel{ID: "client", Title: "Klient", Enabled: true, Children: children}
}

func updateMenu(vm app.ViewModel) MenuItemModel {
	update := vm.Update
	children := []MenuItemModel{
		disabledItem("update.version", fmt.Sprintf("Wersja: %s → %s", update.CurrentVersion, update.AvailableVersion)),
	}
	if strings.TrimSpace(update.Summary) != "" {
		children = append(children, disabledItem("update.summary", update.Summary))
	}
	if vm.CanPlanUpdate() {
		children = append(children, actionItem("update.plan", "Pokaż, co ulegnie zmianie…", "Zweryfikowany dry run bez instalacji", Intent{Kind: IntentUpdatePlan}))
	}
	if vm.CanApplyUpdate() {
		children = append(children, actionItem("update.apply", "Zaktualizuj i uruchom ponownie…", "Instalacja wymaga potwierdzenia", Intent{Kind: IntentUpdateApply}))
	}
	return MenuItemModel{
		ID: "update", Title: "● Dostępna aktualizacja " + update.AvailableVersion,
		Tooltip: "Podpisane wydanie " + update.ReleaseID, Enabled: true, Children: children,
	}
}

func serverMenu(vm app.ViewModel, server app.ServerViewModel) MenuItemModel {
	name := server.DisplayName
	if strings.TrimSpace(name) == "" {
		name = server.ID
	}
	children := []MenuItemModel{}
	children = append(children, actionItem("server."+server.ID+".settings", "Zarządzaj serwerem…", "Informacje o serwerze, foldery i akcje administracyjne", Intent{Kind: IntentSettings, ServerID: server.ID}))
	if server.NeedsRealmAliasClaim() && vm.CanClaimRealmAlias() {
		children = append(children, actionItem("server."+server.ID+".realm_alias", "Ustaw stały alias…", "Ustaw niezmienny pseudonim widoczny przy blokadach", Intent{Kind: IntentSetRealmAlias, ServerID: server.ID}))
	}
	if vm.Connected && !vm.Stale {
		children = append(children, actionItem("server."+server.ID+".pair_mobile", "Sparuj urządzenie mobilne…", "Wygeneruj kod QR do parowania telefonu", Intent{Kind: IntentPairMobileDevice, ServerID: server.ID}))
	}
	visibleRepos := 0
	localFolders := 0
	for _, repo := range server.Repos {
		if !repo.Attached && repo.AttachmentPolicy != "required" {
			continue
		}
		visibleRepos++
		if repo.Attached && strings.TrimSpace(repo.LocalPath) != "" {
			localFolders++
		}
		children = append(children, repoMenu(vm, repo))
	}
	if visibleRepos == 0 {
		children = append(children, disabledItem("server."+server.ID+".empty", "Brak lokalnych folderów FileES"))
	}
	// Always offered, not just before the first folder: the only other path
	// to this action is the "Zarządzaj serwerem…" table, which forces
	// picking an existing folder row first — confusing when the point is to
	// add a folder that does not exist yet.
	if vm.Connected && !vm.Stale && server.CanOfferRepositoryCreation() {
		label, tooltip := "Dodaj pierwszy folder do FileES…", "Utwórz pierwsze lokalne repozytorium na tym serwerze"
		if localFolders > 0 {
			label, tooltip = "Dodaj kolejny folder do FileES…", "Utwórz kolejne lokalne repozytorium na tym serwerze"
		}
		children = append(children, actionItem("server."+server.ID+".create_folder", label, tooltip, Intent{Kind: IntentCreateRepository, ServerID: server.ID}))
	}
	return MenuItemModel{ID: "server." + server.ID, Title: name, Enabled: true, Children: children}
}

func connectionLabel(vm app.ViewModel) string {
	if !vm.Connected {
		return "Brak połączenia"
	}
	if vm.Stale {
		return "Odświeżanie"
	}
	return "Połączono"
}

func repoMenu(vm app.ViewModel, repo app.RepoViewModel) MenuItemModel {
	name := repo.DisplayName
	if strings.TrimSpace(name) == "" {
		name = repo.ID
	}
	title := repoStatusMark(repo) + " " + name
	item := disabledItem("repo."+repo.ID, title)
	item.Tooltip = repo.LocalPath
	if repo.Attached && strings.TrimSpace(repo.LocalPath) != "" {
		lockVisible := repo.CanWrite() && vm.CanMutateLock() && serverHasRealmAlias(vm, repo.ServerID)
		unlockVisible := repo.CanWrite() && vm.CanMutateUnlock()
		item.Enabled = true
		item.Children = append(item.Children,
			actionItem("repo."+repo.ID+".open", "Otwórz folder", repo.LocalPath, Intent{Kind: IntentOpenFolder, RepoID: repo.ID}),
		)
		if lockVisible {
			item.Children = append(item.Children,
				actionItem("repo."+repo.ID+".lock", "Zablokuj pliki…", "Nabierz blokadę lub edit-passport dla wybranych plików", Intent{Kind: IntentLock, RepoID: repo.ID}),
			)
		}
		if unlockVisible {
			if repo.ReservationCount > 0 {
				item.Children = append(item.Children,
					actionItem("repo."+repo.ID+".unlock", "Odblokuj pliki…", "Zwolnij blokadę lub edit-passport wybranych plików", Intent{Kind: IntentUnlock, RepoID: repo.ID}),
				)
			} else {
				item.Children = append(item.Children, disabledItem("repo."+repo.ID+".unlock", "Odblokuj pliki…"))
			}
		}
	}
	return item
}

func serverHasRealmAlias(vm app.ViewModel, serverID string) bool {
	for _, server := range vm.Servers {
		if server.ID == serverID {
			return server.RealmID == "" || server.RealmAlias != ""
		}
	}
	// A repository without a server binding is only possible in a legacy or
	// pre-activation snapshot; do not hide historical controls in that state.
	return true
}

func repositoryOwnedByActiveRealm(vm app.ViewModel, repo app.RepoViewModel) bool {
	for _, server := range vm.Servers {
		if server.ID == repo.ServerID {
			return server.Owns(repo) && server.CanOfferRepositoryCreation()
		}
	}
	return false
}

func repoStatusMark(repo app.RepoViewModel) string {
	switch repo.DisplayState() {
	case app.RepoDisplayActive:
		return "✓"
	case app.RepoDisplayBusy, app.RepoDisplayInitializing, app.RepoDisplayBaselining, app.RepoDisplayPaused, app.RepoDisplayStopping:
		return "◷"
	case app.RepoDisplayOffline, app.RepoDisplayAttention, app.RepoDisplayRevoked:
		return "⚠"
	default:
		return "○"
	}
}

func ownershipLabel(server app.ServerViewModel, repo app.RepoViewModel) string {
	if server.Owns(repo) {
		return "własne"
	}
	if repo.OwnerRealmID != "" {
		return "udostępnione"
	}
	return "własność niepotwierdzona"
}

func attachmentPolicyLabel(policy string) string {
	if policy == "required" {
		return "wymagane przez serwer"
	}
	return "opcjonalne"
}

func accessLabel(access string) string {
	if access == "r" {
		return "tylko odczyt"
	}
	if access == "rw" {
		return "odczyt i zapis"
	}
	return "nieznany"
}

func repoStateLabel(repo app.RepoViewModel) string {
	switch repo.DisplayState() {
	case app.RepoDisplayActive:
		return "Aktywne"
	case app.RepoDisplayBusy:
		return "Praca w toku"
	case app.RepoDisplayInitializing:
		return "Inicjalizacja"
	case app.RepoDisplayBaselining:
		return "Budowanie stanu bazowego"
	case app.RepoDisplayPaused:
		return "Wstrzymane"
	case app.RepoDisplayStopping:
		return "Zatrzymywanie"
	case app.RepoDisplayOffline:
		return "Offline"
	case app.RepoDisplayAttention:
		return "Wymaga uwagi"
	case app.RepoDisplayUnattached:
		return "Nieprzypięte lokalnie"
	case app.RepoDisplayDisabled:
		return "Wyłączone"
	case app.RepoDisplayRevoked:
		return "Dostęp cofnięty"
	default:
		return "Stan nieznany"
	}
}
