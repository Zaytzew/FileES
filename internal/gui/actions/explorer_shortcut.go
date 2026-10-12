package actions

import (
	"context"

	"filees/internal/gui/platform"
)

type ExplorerShortcutManager interface {
	ExplorerShortcut(context.Context, string, string, *bool, string) (ExplorerShortcutPreference, error)
}
type ExplorerShortcutPreference struct {
	Visible bool
	WCID    string
}

func (c *Controller) startExplorerShortcut(ctx context.Context, serverID, repoID string) {
	key := "explorer-shortcut." + serverID + "." + repoID
	if serverID == "" || repoID == "" || c.cfg.ExplorerShortcut == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.Connected || vm.Stale || !vm.CanExplorerShortcut() {
			return
		}
		name := ""
		for _, repo := range vm.Repos {
			if repo.ServerID == serverID && repo.ID == repoID && repo.Attached && repo.ExplorerShortcut != nil {
				name = repo.DisplayName
				break
			}
		}
		if name == "" {
			return
		}
		current, err := c.cfg.ExplorerShortcut.ExplorerShortcut(ctx, serverID, repoID, nil, "")
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("feedback.shortcut.failed", "Nie udało się zmienić skrótu"), Body: c.actionErrorBody(err), Urgency: platform.UrgencyCritical})
			return
		}
		presentation := "confirm.showExplorerShortcut"
		title, text := "Pokazać skrót w panelu Eksploratora?", "Folder „"+name+"” będzie widoczny w lewym panelu Eksploratora. Dotyczy to tylko tej kopii na tym komputerze."
		if current.Visible {
			presentation = "confirm.hideExplorerShortcut"
			title = "Ukryć skrót w panelu Eksploratora?"
			text = "Folder „" + name + "” zniknie z lewego panelu Eksploratora. Folder wirtualny pozostaje aktywny, pliki i synchronizacja bez zmian."
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{PresentationKey: presentation, PresentationArgs: map[string]string{"name": name}, Title: title, Text: text, ConfirmText: "Zastosuj", CancelText: "Anuluj"})
		if err != nil || !confirmed {
			return
		}
		latest := c.cfg.ViewModel()
		if !latest.Connected || latest.Stale || !latest.CanExplorerShortcut() || current.WCID == "" {
			return
		}
		visible := !current.Visible
		stored, err := c.cfg.ExplorerShortcut.ExplorerShortcut(ctx, serverID, repoID, &visible, current.WCID)
		if err != nil || stored.Visible != visible || stored.WCID != current.WCID {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("feedback.shortcut.failed", "Nie udało się zmienić skrótu"), Body: c.uiText("feedback.shortcut.notConfirmed", "Zmiana nie została potwierdzona. Otwórz ustawienia folderu i sprawdź ponownie."), Urgency: platform.UrgencyCritical})
			return
		}
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("feedback.shortcut.changed", "Ustawienie skrótu zapisane"), Body: c.uiText("feedback.shortcut.localOnly", "Dotyczy tylko wybranego folderu na tym komputerze. Pliki i synchronizacja pozostają bez zmian."), Urgency: platform.UrgencyNormal})
	}()
}
