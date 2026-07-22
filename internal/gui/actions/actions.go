// Package actions dispatches tray intents to platform services and the daemon.
// It imports tray (for Intent types), app (for ViewModel and DaemonClient shape),
// and platform — never ipcclient or engine packages.
package actions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
)

// creationStatusPollInterval/creationStatusPollTimeout bound how long the GUI
// waits for a server-side outcome (attached/error) after CreateRepository
// returns. Provisioning runs entirely in the daemon's background queue, so a
// repository creation that fails after the "started" toast (e.g. a storage
// preflight rejection) would otherwise vanish without ever telling the user
// -- see finding on silent repo-create failures.
const (
	creationStatusPollInterval = 3 * time.Second
	creationStatusPollTimeout  = 15 * time.Minute
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

type RepositoryCreator interface {
	CreateRepository(ctx context.Context, serverID, displayName, localPath string) (operationID string, err error)
	// CreationStatus polls the outcome of a prior CreateRepository call.
	// state is the daemon's localrepo lifecycle state ("request_pending",
	// "attached", "error", ...); lastError is populated only once state
	// reaches "error".
	CreationStatus(ctx context.Context, operationID string) (state, lastError string, err error)
}

type Updater interface {
	UpdatePlan(context.Context) (*UpdatePlan, error)
	UpdateApply(context.Context) (*UpdateResult, error)
}

type UpdateChange struct{ Action, Path, Detail string }

type UpdatePlan struct {
	CurrentVersion, AvailableVersion, ReleaseID string
	Changes                                     []UpdateChange
	RestartRequired                             bool
}

type UpdateResult struct {
	InstalledVersion string
	RestartRequired  bool
}

type presentationError interface {
	error
	PresentationError() (code, severity, hint, message string)
}

// Config wires the controller to its dependencies.
// Notifier, Reconnect and Quit may be nil; all other fields are required.
type Config struct {
	Intents           <-chan tray.Intent
	ViewModel         func() app.ViewModel
	Opener            platform.FolderOpener
	Picker            platform.FilePicker
	FolderPicker      platform.FolderPicker
	Prompter          platform.Prompter
	RepositoryCreator RepositoryCreator
	Activator         Activator
	Updater           Updater
	Notifier          platform.Notifier // nil → notifications silently dropped
	Locker            LockUnlocker
	Reconnect         func() // nil → reconnect intent is a no-op
	Quit              func() // nil → quit intent is a no-op
	Restart           func() // called only after a successful apply requiring restart

	// CreationStatusPollInterval/CreationStatusPollTimeout override how often
	// and how long awaitCreationOutcome polls after a repository-create
	// request; zero means use the package defaults (tests may shrink these).
	CreationStatusPollInterval time.Duration
	CreationStatusPollTimeout  time.Duration
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
	case tray.IntentCreateRepository:
		c.startCreateRepository(ctx, intent.ServerID)
	case tray.IntentUpdatePlan:
		c.startUpdate(ctx, false)
	case tray.IntentUpdateApply:
		c.startUpdate(ctx, true)
	case tray.IntentQuit:
		if c.cfg.Quit != nil {
			c.cfg.Quit()
		}
	}
}

func (c *Controller) startCreateRepository(ctx context.Context, serverID string) {
	key := "create-repository:" + serverID
	if serverID == "" || c.cfg.FolderPicker == nil || c.cfg.Prompter == nil || c.cfg.RepositoryCreator == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		var server *app.ServerViewModel
		for i := range vm.Servers {
			if vm.Servers[i].ID == serverID {
				server = &vm.Servers[i]
				break
			}
		}
		if !vm.Connected || vm.Stale || server == nil || !server.CanOfferRepositoryCreation() {
			return
		}
		picked, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: "Wybierz folder dla nowego repozytorium FileES"})
		if err != nil {
			c.repositoryCreationFailure(ctx, err)
			return
		}
		if picked.Cancelled || strings.TrimSpace(picked.Path) == "" {
			return
		}
		name := filepath.Base(filepath.Clean(picked.Path))
		prompted, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Nowe repozytorium FileES", Text: "Nazwa repozytorium:", Placeholder: name})
		if err != nil {
			c.repositoryCreationFailure(ctx, err)
			return
		}
		if prompted.Cancelled {
			return
		}
		displayName := strings.TrimSpace(prompted.Value)
		if displayName == "" {
			displayName = name
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Utwórz repozytorium FileES", Text: fmt.Sprintf("Serwer: %s\nNazwa: %s\nFolder: %s\nDostęp: odczyt i zapis\n\nUtworzyć repozytorium i rozpocząć synchronizację?", server.ID, displayName, picked.Path), ConfirmText: "Utwórz", CancelText: "Anuluj"})
		if err != nil {
			c.repositoryCreationFailure(ctx, err)
			return
		}
		if !confirmed {
			return
		}
		// Re-check live authority immediately before the mutating IPC request.
		latest := c.cfg.ViewModel()
		allowed, found := latest.Connected && !latest.Stale, false
		for _, candidate := range latest.Servers {
			if candidate.ID == serverID {
				found = true
				allowed = allowed && candidate.CanOfferRepositoryCreation()
				break
			}
		}
		if !allowed || !found {
			return
		}
		operationID, err := c.cfg.RepositoryCreator.CreateRepository(ctx, serverID, displayName, picked.Path)
		if err != nil {
			c.repositoryCreationFailure(ctx, err)
			return
		}
		c.notify(ctx, platform.Notification{ID: "repository-create." + serverID, Group: "repository-create." + serverID, Title: "Tworzenie repozytorium rozpoczęte", Body: displayName + " — operacja " + operationID, Urgency: platform.UrgencyNormal})
		c.awaitCreationOutcome(ctx, serverID, displayName, operationID)
	}()
}

// awaitCreationOutcome polls the daemon for the real outcome of a
// repository creation after the optimistic "started" toast, since
// provisioning (storage preflight, repository creation, initial commit) all
// run asynchronously in the daemon and would otherwise fail silently.
func (c *Controller) awaitCreationOutcome(ctx context.Context, serverID, displayName, operationID string) {
	interval, timeout := c.cfg.CreationStatusPollInterval, c.cfg.CreationStatusPollTimeout
	if interval <= 0 {
		interval = creationStatusPollInterval
	}
	if timeout <= 0 {
		timeout = creationStatusPollTimeout
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		state, lastError, err := c.cfg.RepositoryCreator.CreationStatus(ctx, operationID)
		if err != nil {
			return
		}
		switch state {
		case "error":
			body := displayName
			if strings.TrimSpace(lastError) != "" {
				body = displayName + " — " + lastError
			}
			c.notify(ctx, platform.Notification{ID: "repository-create." + serverID, Group: "repository-create." + serverID, Title: "Nie udało się utworzyć repozytorium", Body: body, Urgency: platform.UrgencyCritical})
			return
		case "attached":
			c.notify(ctx, platform.Notification{ID: "repository-create." + serverID, Group: "repository-create." + serverID, Title: "Repozytorium utworzone", Body: displayName, Urgency: platform.UrgencyNormal})
			return
		}
		if time.Now().After(deadline) {
			return
		}
	}
}

func (c *Controller) repositoryCreationFailure(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	c.notify(ctx, platform.Notification{ID: "repository-create", Group: "repository-create", Title: "Nie można utworzyć repozytorium", Body: err.Error(), Urgency: platform.UrgencyCritical})
}

func (c *Controller) startUpdate(ctx context.Context, apply bool) {
	if c.cfg.Updater == nil || c.cfg.Prompter == nil || !c.beginOperation("update") {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("update")
		plan, err := c.cfg.Updater.UpdatePlan(ctx)
		if err != nil {
			c.updateFailure(ctx, "Nie można przygotować planu aktualizacji", err)
			return
		}
		text := updatePlanText(plan)
		if !apply {
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Plan aktualizacji FileES", Text: text})
			return
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
			Title: "Aktualizacja FileES", Text: text + "\n\nZaktualizować i uruchomić FileES ponownie?",
			ConfirmText: "Zaktualizuj", CancelText: "Anuluj",
		})
		if err != nil {
			c.updateFailure(ctx, "Nie można wyświetlić potwierdzenia", err)
			return
		}
		if !confirmed {
			return
		}
		result, err := c.cfg.Updater.UpdateApply(ctx)
		if err != nil {
			c.updateFailure(ctx, "Aktualizacja nie powiodła się", err)
			return
		}
		c.notify(ctx, platform.Notification{ID: "update", Group: "update", Title: "FileES zaktualizowano do wersji " + result.InstalledVersion, Urgency: platform.UrgencyNormal})
		if result.RestartRequired && c.cfg.Restart != nil {
			c.cfg.Restart()
		}
	}()
}

func updatePlanText(plan *UpdatePlan) string {
	if plan == nil {
		return "Daemon nie zwrócił planu aktualizacji."
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Wersja: %s → %s\n", plan.CurrentVersion, plan.AvailableVersion)
	if plan.ReleaseID != "" {
		fmt.Fprintf(&text, "Wydanie: %s\n", plan.ReleaseID)
	}
	if len(plan.Changes) == 0 {
		text.WriteString("\nBrak zmian plików.")
	} else {
		text.WriteString("\nZmiany:\n")
		for _, change := range plan.Changes {
			fmt.Fprintf(&text, "• %s  %s", strings.ToUpper(change.Action), change.Path)
			if change.Detail != "" {
				fmt.Fprintf(&text, " — %s", change.Detail)
			}
			text.WriteByte('\n')
		}
	}
	if plan.RestartRequired {
		text.WriteString("\nPo instalacji wymagane jest ponowne uruchomienie FileES.")
	}
	return strings.TrimSpace(text.String())
}

func (c *Controller) updateFailure(ctx context.Context, title string, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	c.notify(ctx, platform.Notification{ID: "update", Group: "update", Title: title, Body: err.Error(), Urgency: platform.UrgencyCritical})
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
		creation := "niedozwolone"
		if server.CanOfferRepositoryCreation() {
			creation = "dozwolone"
		}
		text := fmt.Sprintf("Alias: %s\nAdres serwera: %s\nPort SSH: %d\nID klienta: %s\nTryb klienta: %s\nTworzenie repozytoriów: %s", server.ID, address, server.SSHPort, clientID, clientRoleDescription(server.ClientRole), creation)
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
		otp, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Wprowadź kod OTP otrzymany e-mailem:", Secret: true})
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
