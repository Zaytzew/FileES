package actions

import (
	"context"
	"filees/internal/gui/platform"
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

func (c *Controller) downloadShelfItem(ctx context.Context, key, serverID, repoID, channelID, uploadID string) {
	downloads, ok := c.cfg.Shelf.(ShelfDownloader)
	if !ok || c.cfg.FolderPicker == nil {
		return
	}
	fail := func(err error) {
		c.reportActionError(ctx, key, c.uiText("feedback.n055", "Nie udało się pobrać pliku"), err.Error())
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
	status, err = downloads.ShelfFetch(ctx, serverID, repoID, channelID, uploadID, localPath, false)
	if err != nil {
		fail(err)
		return
	}
	c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("shelf.download.running", "Pobieranie z półki trwa"), Body: localPath})
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
			c.reportActionError(ctx, key, c.uiText("feedback.n055", "Nie udało się pobrać pliku"), status.Error)
			return
		}
		if status.State == "complete" {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: c.uiText("shelf.download.complete", "Pobrano plik z półki"), Body: status.LocalPath})
			return
		}
	}
}
