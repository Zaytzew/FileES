package actions

import (
	"context"
	"filees/internal/gui/tray"
)

type syncPauser interface {
	SyncPause(context.Context, bool) error
}

func (c *Controller) startSyncPause(ctx context.Context, kind tray.IntentKind) {
	if !c.beginOperation("sync-pause") {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("sync-pause")
		pauser, ok := c.cfg.Shouts.(syncPauser)
		if !ok {
			return
		}
		if err := pauser.SyncPause(ctx, kind == tray.IntentPauseSync); err != nil {
			c.reportActionError(ctx, "sync-pause", c.uiText("pause.failed", "Nie udało się zmienić pauzy"), c.actionErrorBody(err))
		}
		if c.cfg.Refresh != nil {
			c.cfg.Refresh()
		}
	}()
}
