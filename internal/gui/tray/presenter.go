package tray

import (
	"fmt"
	"strings"

	app "filees/internal/gui/app"
)

// BuildMenu converts an app ViewModel into a deterministic tray menu.
func BuildMenu(vm app.ViewModel) MenuModel {
	active := len(vm.Servers) > 0
	status := connectionLabel(vm)
	model := MenuModel{
		Icon:    vm.Icon,
		Title:   "FileES — " + status,
		Tooltip: fmt.Sprintf("FileES — %s — repozytoria: %d", status, len(vm.Repos)),
	}
	if active {
		model.Title += " — Klient aktywowany"
		model.Tooltip += " — klient aktywowany"
	}
	model.Items = append(model.Items,
		disabledItem("system.status", model.Title),
		disabledItem("system.refreshed", lastRefreshLabel(vm)),
	)
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

	if vm.CanListErrors() {
		model.Items = append(model.Items, separator("sep.errors"), errorsMenu(vm.Errors))
	}
	model.Items = append(model.Items,
		separator("sep.actions"),
		actionItem("action.activate", "Aktywuj klienta na nowym serwerze…", "Dodaj aktywację FileES kodem z e-maila", Intent{Kind: IntentActivate}),
		actionItem("action.reconnect", "Połącz ponownie", "Odśwież połączenie z daemonem", Intent{Kind: IntentReconnect}),
		actionItem("action.quit", "Zamknij GUI", "Zamknij tylko aplikację tray", Intent{Kind: IntentQuit}),
	)
	return model
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
	children := []MenuItemModel{
		actionItem("server."+server.ID+".info", "Informacje o serwerze…", "Pokaż adres, identyfikatory i uprawnienia klienta", Intent{Kind: IntentServerInfo, ServerID: server.ID}),
	}
	if vm.Connected && !vm.Stale && server.CanOfferRepositoryCreation() {
		children = append(children, actionItem("server."+server.ID+".create", "Dodaj folder do FileES…", "Utwórz nowe repozytorium z lokalnego katalogu", Intent{Kind: IntentCreateRepository, ServerID: server.ID}))
	}
	visibleRepos := 0
	for _, repo := range server.Repos {
		if !repo.Attached && repo.AttachmentPolicy != "required" {
			continue
		}
		visibleRepos++
		children = append(children, repoMenu(repo))
	}
	if visibleRepos == 0 {
		children = append(children, disabledItem("server."+server.ID+".empty", "Brak lokalnych folderów FileES"))
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

func lastRefreshLabel(vm app.ViewModel) string {
	if vm.LastRefresh.IsZero() {
		return "Ostatnia aktualizacja: brak"
	}
	label := "Ostatnia aktualizacja: " + vm.LastRefresh.Local().Format("15:04:05")
	if vm.Stale || !vm.Connected {
		label += " (dane nieaktualne)"
	}
	return label
}

func repoMenu(repo app.RepoViewModel) MenuItemModel {
	name := repo.DisplayName
	if strings.TrimSpace(name) == "" {
		name = repo.ID
	}
	title := repoStatusMark(repo) + " " + name
	item := disabledItem("repo."+repo.ID, title)
	item.Tooltip = repo.LocalPath
	if repo.Attached && strings.TrimSpace(repo.LocalPath) != "" {
		item.Enabled = true
		item.Intent = &Intent{Kind: IntentOpenFolder, RepoID: repo.ID}
	}
	return item
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

func errorsMenu(errors []app.ErrorViewModel) MenuItemModel {
	children := make([]MenuItemModel, 0, len(errors))
	for i := len(errors) - 1; i >= 0; i-- {
		record := errors[i]
		title := fmt.Sprintf("[%s] %s — %s", record.Severity, record.Code, record.Message)
		item := disabledItem(fmt.Sprintf("error.%d.%s", i, record.ID), title)
		item.Tooltip = errorHintLabel(record.Hint)
		children = append(children, item)
	}
	if len(children) == 0 {
		children = append(children, disabledItem("errors.empty", "Brak błędów"))
	}
	return MenuItemModel{ID: "errors", Title: "Ostatnie błędy", Enabled: true, Children: children}
}

func errorHintLabel(hint string) string {
	switch hint {
	case "RETRY_LOCAL":
		return "Spróbuj ponownie"
	case "RETRY_BACKOFF":
		return "Ponowienie nastąpi później"
	case "REQUIRE_ACTION":
		return "Wymagane działanie użytkownika"
	case "ADMIN_ONLY":
		return "Skontaktuj się z administratorem"
	default:
		return ""
	}
}
