package tray

import (
	"fmt"
	"strings"

	app "filees/internal/gui/app"
)

// BuildMenu converts an app ViewModel into a deterministic tray menu.
func BuildMenu(vm app.ViewModel) MenuModel {
	if len(vm.Servers) == 0 && len(vm.Repos) > 0 {
		vm.Servers = []app.ServerViewModel{{ID: "default", DisplayName: "Serwer", ClientRole: "normal", Repos: vm.Repos}}
	}
	status := connectionLabel(vm)
	model := MenuModel{
		Icon:    vm.Icon,
		Title:   "FileES — " + status,
		Tooltip: fmt.Sprintf("FileES — %s — repozytoria: %d", status, len(vm.Repos)),
	}
	model.Items = append(model.Items,
		disabledItem("system.status", model.Title),
		disabledItem("system.refreshed", lastRefreshLabel(vm)),
		separator("sep.repositories"),
	)

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
		actionItem("action.activate", "Aktywuj klienta…", "Rozpocznij aktywację serwera kodem z e-maila", Intent{Kind: IntentActivate}),
		actionItem("action.reconnect", "Połącz ponownie", "Odśwież połączenie z daemonem", Intent{Kind: IntentReconnect}),
		actionItem("action.quit", "Zamknij GUI", "Zamknij tylko aplikację tray", Intent{Kind: IntentQuit}),
	)
	return model
}

func serverMenu(vm app.ViewModel, server app.ServerViewModel) MenuItemModel {
	name := server.DisplayName
	if strings.TrimSpace(name) == "" {
		name = server.ID
	}
	children := []MenuItemModel{disabledItem("server."+server.ID+".role", "Tryb klienta: "+clientRoleLabel(server.ClientRole))}
	if len(server.Repos) == 0 {
		children = append(children, disabledItem("server."+server.ID+".empty", "Brak repozytoriów"))
	}
	for _, repo := range server.Repos {
		children = append(children, repoMenu(vm, repo))
	}
	return MenuItemModel{ID: "server." + server.ID, Title: name, Enabled: true, Children: children}
}

func clientRoleLabel(role string) string {
	if role == "ro" {
		return "tylko odczyt"
	}
	return "pełny"
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

func repoMenu(vm app.ViewModel, repo app.RepoViewModel) MenuItemModel {
	title := fmt.Sprintf("%s — %s", repo.ID, repoStateLabel(repo))
	pending := repo.Pending.Added + repo.Pending.Modified + repo.Pending.Deleted
	children := []MenuItemModel{
		disabledItem("repo."+repo.ID+".state", "Stan: "+repoStateLabel(repo)),
		disabledItem("repo."+repo.ID+".revision", fmt.Sprintf("Rewizja: %d / %d", repo.LocalRev, repo.HeadRev)),
		disabledItem("repo."+repo.ID+".pending", fmt.Sprintf("Oczekujące zmiany: %d", pending)),
		separator("repo." + repo.ID + ".sep.actions"),
	}
	open := actionItem("repo."+repo.ID+".open", "Otwórz katalog", repo.LocalPath,
		Intent{Kind: IntentOpenFolder, RepoID: repo.ID})
	if strings.TrimSpace(repo.LocalPath) == "" {
		open.Enabled = false
		open.Intent = nil
	}
	children = append(children, open)
	children = append(children, disabledItem("repo."+repo.ID+".access", "Dostęp: "+accessLabel(repo.Access)))
	if vm.CanMutateLock() && repo.CanWrite() {
		children = append(children, actionItem("repo."+repo.ID+".lock", "Zablokuj pliki…", "Wybierz pliki do zablokowania",
			Intent{Kind: IntentLock, RepoID: repo.ID}))
	}
	if vm.CanMutateUnlock() && repo.CanWrite() {
		children = append(children, actionItem("repo."+repo.ID+".unlock", "Odblokuj pliki…", "Wybierz pliki do odblokowania",
			Intent{Kind: IntentUnlock, RepoID: repo.ID}))
	}
	return MenuItemModel{ID: "repo." + repo.ID, Title: title, Enabled: true, Children: children}
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
