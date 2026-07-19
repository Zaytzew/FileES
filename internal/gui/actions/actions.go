// Package actions dispatches tray intents to platform services and the daemon.
// It imports tray (for Intent types), app (for ViewModel and DaemonClient shape),
// and platform — never ipcclient or engine packages.
package actions

import (
	"context"
	"errors"
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

type Activator interface {
	Begin(ctx context.Context, serverID, serverAddress, email string) error
	Finish(ctx context.Context, serverID, serverAddress string, otp []byte) error
}

type presentationError interface {
	error
	PresentationError() (code, severity, hint, message string)
}

// Config wires the controller to its dependencies.
// Notifier, Reconnect and Quit may be nil; all other fields are required.
type Config struct {
	Intents   <-chan tray.Intent
	ViewModel func() app.ViewModel
	Opener    platform.FolderOpener
	Picker    platform.FilePicker
	Prompter  platform.Prompter
	Activator Activator
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
	tasks        sync.WaitGroup
}

// New creates a Controller with the given configuration.
func New(cfg Config) *Controller {
	return &Controller{cfg: cfg, operations: make(map[string]struct{})}
}

// Run processes intents until ctx is cancelled or the intents channel closes.
func (c *Controller) Run(ctx context.Context) {
	defer c.tasks.Wait()
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
		c.startOpenFolder(ctx, intent.RepoID)
	case tray.IntentLock:
		c.startLockUnlock(ctx, intent.RepoID, true)
	case tray.IntentUnlock:
		c.startLockUnlock(ctx, intent.RepoID, false)
	case tray.IntentReconnect:
		if c.cfg.Reconnect != nil {
			c.cfg.Reconnect()
		}
	case tray.IntentActivate:
		c.startActivation(ctx)
	case tray.IntentServerInfo:
		c.startServerInfo(ctx, intent.ServerID)
	case tray.IntentQuit:
		if c.cfg.Quit != nil {
			c.cfg.Quit()
		}
	}
}

func (c *Controller) startServerInfo(ctx context.Context, serverID string) {
	if c.cfg.Prompter == nil || serverID == "" {
		return
	}
	for _, server := range c.cfg.ViewModel().Servers {
		if server.ID != serverID {
			continue
		}
		address := server.Address
		if address == "" {
			address = "brak danych"
		}
		clientID := server.ClientID
		if clientID == "" {
			clientID = "brak danych"
		}
		text := fmt.Sprintf("Alias: %s\nAdres serwera: %s\nPort SSH: %d\nID klienta: %s\nTryb klienta: %s", server.ID, address, server.SSHPort, clientID, clientRoleDescription(server.ClientRole))
		c.tasks.Add(1)
		go func() {
			defer c.tasks.Done()
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Serwer FileES — " + server.ID, Text: text})
		}()
		return
	}
}

func clientRoleDescription(role string) string {
	if role == "ro" {
		return "tylko odczyt"
	}
	return "pełny"
}

func (c *Controller) startActivation(ctx context.Context) {
	if c.cfg.Prompter == nil || c.cfg.Activator == nil || !c.beginOperation("activate") {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("activate")
		profile, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Lokalna nazwa profilu serwera (np. biuro):", Placeholder: "biuro"})
		if err != nil || profile.Cancelled || profile.Value == "" {
			c.activationFailure(ctx, err)
			return
		}
		endpoint, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Adres serwera FileES:", Placeholder: "filees.example.net"})
		if err != nil || endpoint.Cancelled || endpoint.Value == "" {
			c.activationFailure(ctx, err)
			return
		}
		email, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Adres e-mail, na który serwer wyśle jednorazowy kod OTP:"})
		if err != nil || email.Cancelled || email.Value == "" {
			c.activationFailure(ctx, err)
			return
		}
		if err := c.cfg.Activator.Begin(ctx, profile.Value, endpoint.Value, email.Value); err != nil {
			c.activationFailure(ctx, err)
			return
		}
		otp, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Wklej kod OTP otrzymany e-mailem:", Secret: true})
		if err != nil || otp.Cancelled || otp.Value == "" {
			c.activationFailure(ctx, err)
			return
		}
		secret := []byte(otp.Value)
		defer clear(secret)
		if err := c.cfg.Activator.Finish(ctx, profile.Value, endpoint.Value, secret); err != nil {
			c.activationFailure(ctx, err)
			return
		}
		c.notify(ctx, platform.Notification{ID: "activation", Group: "activation", Title: "Klient FileES aktywowany na serwerze", Body: endpoint.Value, Urgency: platform.UrgencyNormal})
	}()
}

func (c *Controller) activationFailure(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	c.notify(ctx, platform.Notification{ID: "activation", Group: "activation", Title: "Aktywacja FileES nie powiodła się", Body: err.Error(), Urgency: platform.UrgencyCritical})
}

func (c *Controller) startOpenFolder(ctx context.Context, repoID string) {
	key := "open:" + repoID
	if repoID == "" || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		c.handleOpenFolder(ctx, repoID)
	}()
}

func (c *Controller) startLockUnlock(ctx context.Context, repoID string, lock bool) {
	key := "mutate:" + repoID
	if repoID == "" || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		c.handleLockUnlock(ctx, repoID, lock)
	}()
}

func (c *Controller) beginOperation(key string) bool {
	c.operationsMu.Lock()
	defer c.operationsMu.Unlock()
	if _, busy := c.operations[key]; busy {
		return false
	}
	c.operations[key] = struct{}{}
	return true
}

func (c *Controller) endOperation(key string) {
	c.operationsMu.Lock()
	delete(c.operations, key)
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
	if !ok || repo.LocalPath == "" || !repo.CanWrite() {
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
	if !ok || !currentRepo.CanWrite() || currentRepo.LocalPath == "" || filepath.Clean(currentRepo.LocalPath) != filepath.Clean(repo.LocalPath) {
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
		title, body, urgency := operationErrorPresentation(opName, opErr)
		c.notify(ctx, platform.Notification{
			ID:      opName + "." + repoID,
			Group:   opName + "." + repoID,
			Title:   title,
			Body:    body,
			Urgency: urgency,
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
	// The caller checks the targeted repository separately; this global gate
	// only represents daemon connectivity and command availability.
	if lock {
		return vm.CanMutateLock()
	}
	return vm.CanMutateUnlock()
}

func operationErrorPresentation(opName string, err error) (string, string, platform.Urgency) {
	title := fmt.Sprintf("Błąd operacji (%s)", opName)
	body := err.Error()
	urgency := platform.UrgencyNormal
	var structured presentationError
	if !errors.As(err, &structured) {
		return title, body, urgency
	}
	code, severity, hint, message := structured.PresentationError()
	if code != "" {
		title = fmt.Sprintf("Błąd operacji (%s) — %s", opName, code)
	}
	body = messageLabel(message)
	if label := hintLabel(hint); label != "" {
		body += " — " + label
	}
	if severity == "FATAL" || severity == "ERROR" {
		urgency = platform.UrgencyCritical
	}
	return title, body, urgency
}

func messageLabel(messageKey string) string {
	switch messageKey {
	case "lock.invalid_path":
		return "Wybrana ścieżka nie należy do repozytorium"
	case "lock.operation_failed":
		return "Daemon nie wykonał operacji na plikach"
	case "proto.invalid_payload":
		return "Daemon odrzucił nieprawidłowe dane operacji"
	default:
		return "Błąd zgłoszony przez daemon"
	}
}

func hintLabel(hint string) string {
	switch hint {
	case "RETRY_LOCAL":
		return "spróbuj ponownie"
	case "RETRY_BACKOFF":
		return "ponowienie nastąpi później"
	case "REQUIRE_ACTION":
		return "wymagane działanie użytkownika"
	case "ADMIN_ONLY":
		return "skontaktuj się z administratorem"
	default:
		return ""
	}
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
