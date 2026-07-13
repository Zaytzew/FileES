// Package actions dispatches tray intents to platform services and the daemon.
// It imports tray (for Intent types), app (for ViewModel and DaemonClient shape),
// and platform — never ipcclient or engine packages.
package actions

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

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

	operationsMu sync.Mutex
	operations   map[string]struct{}
}

// New creates a Controller with the given configuration.
func New(cfg Config) *Controller {
	return &Controller{cfg: cfg, operations: make(map[string]struct{})}
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
		c.startLockUnlock(ctx, intent.RepoID, true)
	case tray.IntentUnlock:
		c.startLockUnlock(ctx, intent.RepoID, false)
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

func (c *Controller) startLockUnlock(ctx context.Context, repoID string, lock bool) {
	if repoID == "" || !c.beginRepoOperation(repoID) {
		return
	}
	go func() {
		defer c.endRepoOperation(repoID)
		c.handleLockUnlock(ctx, repoID, lock)
	}()
}

func (c *Controller) beginRepoOperation(repoID string) bool {
	c.operationsMu.Lock()
	defer c.operationsMu.Unlock()
	if _, busy := c.operations[repoID]; busy {
		return false
	}
	c.operations[repoID] = struct{}{}
	return true
}

func (c *Controller) endRepoOperation(repoID string) {
	c.operationsMu.Lock()
	delete(c.operations, repoID)
	c.operationsMu.Unlock()
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
	if !canMutate(vm, lock) {
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
		c.notify(ctx, platform.Notification{
			ID:      opName + "." + repoID,
			Group:   opName + "." + repoID,
			Title:   "Błąd wyboru plików",
			Body:    err.Error(),
			Urgency: platform.UrgencyNormal,
		})
		return
	}
	if result.Cancelled || len(result.Paths) == 0 {
		return
	}

	// Re-check the full mutable state: the daemon or repository configuration
	// may have changed while the native picker was open.
	vm = c.cfg.ViewModel()
	if !canMutate(vm, lock) {
		return
	}
	currentRepo, ok := findRepo(vm, repoID)
	if !ok || currentRepo.LocalPath == "" || filepath.Clean(currentRepo.LocalPath) != filepath.Clean(repo.LocalPath) {
		return
	}
	paths, err := platform.ValidatePickedPaths(currentRepo.LocalPath, result.Paths)
	if err != nil || len(paths) == 0 {
		if err != nil {
			c.notify(ctx, platform.Notification{
				ID:      opName + "." + repoID,
				Group:   opName + "." + repoID,
				Title:   "Nieprawidłowy wybór plików",
				Body:    err.Error(),
				Urgency: platform.UrgencyNormal,
			})
		}
		return
	}

	var opErr error
	if lock {
		_, opErr = c.cfg.Locker.Lock(ctx, repoID, paths)
	} else {
		_, opErr = c.cfg.Locker.Unlock(ctx, repoID, paths)
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
		Title:   fmt.Sprintf("%s %d plik(ów)", successNoun, len(paths)),
		Body:    repoID,
		Urgency: platform.UrgencyLow,
	})
}

func canMutate(vm app.ViewModel, lock bool) bool {
	if !vm.Connected || vm.Stale {
		return false
	}
	if lock {
		return vm.CanLock()
	}
	return vm.CanUnlock()
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
