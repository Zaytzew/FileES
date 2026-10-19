package actions

import (
	"context"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"time"
)

type RepositoryConverter interface {
	WCConversion(context.Context, contract.WCConversionPayload) (*contract.WCConversionStatus, error)
}

func conversionAllowed(vm app.ViewModel, repo app.RepoViewModel, action string) bool {
	if !vm.Connected || vm.Stale || !vm.HasCap(contract.CapWCConversion) || repo.ServerDeleted || repo.Purpose != "" || repo.Conversion == nil {
		return false
	}
	p := repo.Conversion
	if p.ServerID != repo.ServerID || p.RepoID != repo.ID || p.SourcePath != repo.LocalPath {
		return false
	}
	switch action {
	case "begin":
		if vm.VirtualFolders == nil || !vm.VirtualFolders.Servers[repo.ServerID] {
			return false
		}
		return p.CanBegin && repo.Attached && !repo.Sparse && repo.DisplayState() == app.RepoDisplayActive
	case "retry":
		return p.OperationID != "" && p.CanRetry && !p.Running
	case "rollback":
		return p.OperationID != "" && p.CanRollback && !p.Running
	}
	return false
}
func (c *Controller) startWCConversion(ctx context.Context, serverID, repoID, action string) {
	key := "wc-conversion:" + serverID + ":" + repoID
	if c.cfg.RepositoryConverter == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		repo, ok := findRepo(c.cfg.ViewModel(), repoID)
		if !ok || repo.ServerID != serverID || !conversionAllowed(c.cfg.ViewModel(), repo, action) {
			return
		}
		name := firstNonBlank(repo.DisplayName, repo.ID)
		body := c.uiText("conversion.confirm.begin", "FileES poczeka na publikację zmian, zachowa pliki ignorowane i zamieni tę kopię na folder wirtualny w tej samej lokalizacji. Po weryfikacji usunie starą kopię. Zamknij programy korzystające z folderu. Repozytorium na serwerze nie zmieni się.")
		if action == "retry" {
			body = c.uiText("conversion.confirm.retry", "Wznowić tę samą konwersję od zapisanego etapu? FileES ponownie sprawdzi warunki. Nie usuwaj samodzielnie ścieżki odzysku.")
		}
		if action == "rollback" {
			body = c.uiText("conversion.confirm.rollback", "Przywrócić starą kopię roboczą? Nowy punkt wraz z ewentualnymi nowymi plikami zostanie zachowany osobno. Wycofanie jest możliwe tylko przed rozpoczęciem sprzątania starej kopii.")
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: c.uiText("conversion.title", "Konwersja folderu"), Text: name + "\n" + repo.LocalPath + "\n\n" + body, ConfirmText: c.uiText("conversion.confirm", "Potwierdź"), CancelText: c.uiText("conversion.cancel", "Anuluj")})
		if err != nil || !confirmed {
			return
		}
		fresh, ok := findRepo(c.cfg.ViewModel(), repoID)
		if !ok || fresh.ServerID != serverID || fresh.LocalPath != repo.LocalPath || !conversionAllowed(c.cfg.ViewModel(), fresh, action) || fresh.Conversion.OperationID != repo.Conversion.OperationID {
			return
		}
		request := contract.WCConversionPayload{ServerID: serverID, RepoID: repoID, Action: action}
		if action == "begin" {
			request.SourcePath = repo.LocalPath
		} else {
			request.OperationID = repo.Conversion.OperationID
		}
		callCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		result, err := c.cfg.RepositoryConverter.WCConversion(callCtx, request)
		// A lost ACK is uncertain, not permission to repeat a mutating call.
		if err != nil || result == nil || result.ServerID != serverID || result.RepoID != repoID || result.SourcePath != repo.LocalPath || result.OperationID == "" {
			body := c.uiText("conversion.notConfirmed", "Nie potwierdzono przyjęcia dyspozycji. Odśwież stan przed ponowieniem.")
			if err != nil {
				body += "\n" + c.actionErrorBody(err)
			}
			c.reportActionError(ctx, key, c.uiText("conversion.title", "Konwersja folderu"), body)
		} else {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("conversion.accepted", "Dyspozycja konwersji przyjęta"), Body: name + "\n" + repo.LocalPath, Urgency: platform.UrgencyNormal})
		}
		if c.cfg.Refresh != nil {
			c.cfg.Refresh()
		}
	}()
}
