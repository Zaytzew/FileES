// Package actions dispatches tray intents to platform services and the daemon.
// It imports tray (for Intent types), app (for ViewModel and DaemonClient shape),
// and platform — never ipcclient or engine packages.
package actions

import (
	"context"
	"fmt"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
)

// LockUnlocker is the narrow daemon surface required by the controller.
// app.DaemonClient satisfies this interface; the composition root supplies
// the concrete implementation so actions never imports ipcclient.
type LockUnlocker interface {
	Lock(ctx context.Context, repoID string, paths []string) (string, error)
	Unlock(ctx context.Context, repoID string, paths []string) (string, error)
}

// Config wires the controller to its dependencies.
// Notifier, Reconnect and Quit may be nil; all other fields are required.
type Config struct {
	Intents   <-chan tray.Intent
	ViewModel func() app.ViewModel
	Opener    platform.FolderOpener
	Picker    platform.FilePicker
	Notifier  platform.Notifier // nil → notifications silently dropped
	Locker    LockUnlocker
	Reconnect func() // nil → reconnect intent is a no-op
	Quit      func() // nil → quit intent is a no-op
}

// Controller reads tray intents and dispatches them to platform and daemon
// operations. Platform I/O and IPC calls run in dedicated goroutines so that
// a slow or blocked operation never stalls intent delivery.
type Controller struct {
	cfg Config
}

// New creates a Controller with the given configuration.
func New(cfg Config) *Controller {
	return &Controller{cfg: cfg}
}

// Run processes intents until ctx is cancelled or the intents channel closes.
func (c *Controller) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case intent, ok := <-c.cfg.Intents:
			if !ok {
				return
			}
			c.dispatch(ctx, intent)
		}
	}
}

func (c *Controller) dispatch(ctx context.Context, intent tray.Intent) {
	switch intent.Kind {
	case tray.IntentOpenFolder:
		go c.handleOpenFolder(ctx, intent.RepoID)
	case tray.IntentLock:
		go c.handleLockUnlock(ctx, intent.RepoID, true)
	case tray.IntentUnlock:
		go c.handleLockUnlock(ctx, intent.RepoID, false)
	case tray.IntentReconnect:
		if c.cfg.Reconnect != nil {
			c.cfg.Reconnect()
		}
	case tray.IntentQuit:
		if c.cfg.Quit != nil {
			c.cfg.Quit()
		}
	}
}

func (c *Controller) handleOpenFolder(ctx context.Context, repoID string) {
	vm := c.cfg.ViewModel()
	repo, ok := findRepo(vm, repoID)
	if !ok || repo.LocalPath == "" {
		return
	}
	if err := c.cfg.Opener.OpenFolder(ctx, repo.LocalPath); err != nil {
		if ctx.Err() != nil {
			return
		}
		c.notify(ctx, platform.Notification{
			ID:      "open_folder." + repoID,
			Group:   "open_folder." + repoID,
			Title:   "Błąd otwierania katalogu",
			Body:    fmt.Sprintf("%s: %v", repo.LocalPath, err),
			Urgency: platform.UrgencyNormal,
		})
	}
}

func (c *Controller) handleLockUnlock(ctx context.Context, repoID string, lock bool) {
	vm := c.cfg.ViewModel()
	if lock && !vm.CanLock() {
		return
	}
	if !lock && !vm.CanUnlock() {
		return
	}
	repo, ok := findRepo(vm, repoID)
	if !ok || repo.LocalPath == "" {
		return
	}

	var opName, pickerTitle, successNoun string
	if lock {
		opName, pickerTitle, successNoun = "lock", "Zablokuj pliki", "Zablokowano"
	} else {
		opName, pickerTitle, successNoun = "unlock", "Odblokuj pliki", "Odblokowano"
	}

	result, err := c.cfg.Picker.PickFiles(ctx, platform.PickFilesRequest{
		Title:         pickerTitle,
		Root:          repo.LocalPath,
		InitialDir:    repo.LocalPath,
		AllowMultiple: true,
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// FailureUnavailable means the picker tool is absent; no actionable
		// notification for the user since they cannot install it mid-session.
		if !platform.IsFailure(err, platform.FailureUnavailable) {
			c.notify(ctx, platform.Notification{
				ID:      opName + "." + repoID,
				Group:   opName + "." + repoID,
				Title:   "Błąd wyboru plików",
				Body:    err.Error(),
				Urgency: platform.UrgencyNormal,
			})
		}
		return
	}
	if result.Cancelled || len(result.Paths) == 0 {
		return
	}

	// Re-check capability: the daemon may have disconnected while the picker
	// was open. Stale capability confirmation must not trigger a daemon call.
	vm = c.cfg.ViewModel()
	if lock && !vm.CanLock() {
		return
	}
	if !lock && !vm.CanUnlock() {
		return
	}

	var opErr error
	if lock {
		_, opErr = c.cfg.Locker.Lock(ctx, repoID, result.Paths)
	} else {
		_, opErr = c.cfg.Locker.Unlock(ctx, repoID, result.Paths)
	}
	if ctx.Err() != nil {
		return
	}
	if opErr != nil {
		c.notify(ctx, platform.Notification{
			ID:      opName + "." + repoID,
			Group:   opName + "." + repoID,
			Title:   fmt.Sprintf("Błąd operacji (%s)", opName),
			Body:    opErr.Error(),
			Urgency: platform.UrgencyNormal,
		})
		return
	}
	c.notify(ctx, platform.Notification{
		ID:      opName + "." + repoID,
		Group:   opName + "." + repoID,
		Title:   fmt.Sprintf("%s %d plik(ów)", successNoun, len(result.Paths)),
		Body:    repoID,
		Urgency: platform.UrgencyLow,
	})
}

func (c *Controller) notify(ctx context.Context, n platform.Notification) {
	if c.cfg.Notifier == nil {
		return
	}
	_ = c.cfg.Notifier.Notify(ctx, n)
}

func findRepo(vm app.ViewModel, repoID string) (app.RepoViewModel, bool) {
	for _, r := range vm.Repos {
		if r.ID == repoID {
			return r, true
		}
	}
	return app.RepoViewModel{}, false
}
