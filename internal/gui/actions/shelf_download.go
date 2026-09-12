package actions

import (
	"context"
	"filees/internal/gui/platform"
	"fmt"
	"time"
)

func (c *Controller) shelfStatusText(state string) string {
	switch state {
	case "queued", "running":
		return c.uiText("shelf.download.running", "Pobieranie z półki trwa")
	case "complete":
		return c.uiText("shelf.download.complete", "Pobrano plik z półki")
	case "failed":
		return c.uiText("feedback.n055", "Nie udało się pobrać pliku")
	}
	return ""
}

type ShelfImporter interface {
	ShelfImport(context.Context, string, string, string, string, string, string) (ShelfDownload, error)
}

func (c *Controller) downloadShelfItem(ctx context.Context, key, serverID, repoID, channelID, uploadID string, importing ...bool) {
	downloads, ok := c.cfg.Shelf.(ShelfDownloader)
	if !ok || c.cfg.FolderPicker == nil {
		return
	}
	fail := func(err error) {
		title := c.uiText("feedback.n055", "Nie udało się pobrać pliku")
		if len(importing) > 0 && importing[0] {
			title = c.uiText("shelf.import.failed", "Nie udało się osadzić pliku")
		}
		c.reportActionError(ctx, key, title, err.Error())
	}
	status, err := downloads.ShelfFetch(ctx, serverID, repoID, channelID, "", "", true)
	if err != nil {
		fail(err)
		return
	}
	if status.State == "queued" || status.State == "running" {
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("shelf.download.running", "Pobieranie z półki trwa"), Body: status.LocalPath})
		return
	}
	localPath := status.LocalPath
	destination := ""
	if len(importing) > 0 && importing[0] {
		if !status.CanImport {
			return
		}
		if _, ok := c.cfg.Shelf.(ShelfImporter); !ok {
			return
		}
		picked, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: c.uiText("shelf.import.destination", "Wybierz folder docelowy wewnątrz WC macierzystej"), InitialDir: status.ParentPath})
		if err != nil {
			fail(err)
			return
		}
		if picked.Cancelled || picked.Path == "" {
			return
		}
		destination = picked.Path
	}
	if localPath == "" {
		picked, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: c.uiText("picker.shelf", "Wybierz folder półki")})
		if err != nil {
			fail(err)
			return
		}
		if picked.Cancelled || picked.Path == "" {
			return
		}
		localPath = picked.Path
	}
	if destination != "" {
		status, err = c.cfg.Shelf.(ShelfImporter).ShelfImport(ctx, serverID, repoID, channelID, uploadID, localPath, destination)
	} else {
		status, err = downloads.ShelfFetch(ctx, serverID, repoID, channelID, uploadID, localPath, false)
	}
	if err != nil {
		fail(err)
		return
	}
	runningTitle := c.uiText("shelf.download.running", "Pobieranie z półki trwa")
	if destination != "" {
		runningTitle = c.uiText("shelf.import.running", "Pobieranie i osadzanie pliku")
	}
	c.notify(ctx, platform.Notification{ID: key, Group: key, Title: runningTitle, Body: localPath})
	operationID, fetchID := status.OperationID, status.FetchID
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return // only presentation ends; daemon owns the transfer
		case <-ticker.C:
		}
		status, err = downloads.ShelfFetchStatus(ctx, operationID)
		if err != nil {
			fail(err)
			return
		}
		if status.FetchID != fetchID {
			return
		}
		if status.State == "failed" {
			fail(fmt.Errorf("%s", status.Error))
			return
		}
		if status.State == "complete" {
			title := c.uiText("shelf.download.complete", "Pobrano plik z półki")
			if destination != "" {
				title = c.uiText("shelf.import.complete", "Plik osadzony w folderze macierzystym")
			}
			body := status.LocalPath
			if status.Destination != "" {
				body = status.Destination
			}
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: title, Body: body})
			return
		}
	}
}
