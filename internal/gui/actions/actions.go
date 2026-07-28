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
	"filees/pkg/localpin"
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

// ReservationManager is the server-scoped, token-fenced surface used by the
// native reservation browser. The daemon remains the sole SVN caller.
type ReservationManager interface {
	ListReservations(context.Context, string) ([]app.Reservation, error)
	ReleaseReservation(context.Context, app.ReservationReleaseRequest) error
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

type RepositoryDetacher interface {
	DetachRepository(context.Context, string, string, bool) error
}

type StackLifecycle interface {
	RestartFileES(context.Context) error
	ShutdownFileES(context.Context) error
}

// MobilePairingLauncher fetches a mobile pairing token from the daemon and
// hands it to the separate pairing-helper process, which renders it as a QR
// code and handles its own PIN gate and UI - unlike RepositoryCreator, no
// polling/outcome-tracking is needed here: the helper process is itself the
// long-running, user-facing surface.
type MobilePairingLauncher interface {
	Launch(ctx context.Context, serverID string) error
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
// Notifier and Reconnect may be nil; all other fields are required.
type Config struct {
	Intents            <-chan tray.Intent
	ViewModel          func() app.ViewModel
	Opener             platform.FolderOpener
	Picker             platform.FilePicker
	FolderPicker       platform.FolderPicker
	Prompter           platform.Prompter
	RepositoryCreator  RepositoryCreator
	RepositoryDetacher RepositoryDetacher
	MobilePairer       MobilePairingLauncher
	// PinStore, if non-nil, offers PIN setup at the end of a successful
	// activation (see startActivation) - nil means the local-PIN feature is
	// disabled entirely (e.g. platform without a durable state root).
	PinStore           *localpin.Store
	Activator          Activator
	Updater            Updater
	Stack              StackLifecycle
	Notifier           platform.Notifier // nil → notifications silently dropped
	Locker             LockUnlocker
	Reservations       ReservationManager
	ReservationBrowser platform.ReservationBrowser
	Reconnect          func() // nil → reconnect intent is a no-op
	// Refresh obtains a fresh daemon snapshot without reconnecting. It is used
	// after a successful mutation whose result changes tray eligibility.
	Refresh func()
	// PrepareRestart suppresses the intentional daemon disconnect before a
	// user-confirmed restart request reaches IPC. AbortRestart restores normal
	// notifications if that request is rejected.
	PrepareRestart func()
	AbortRestart   func()
	Restart        func() // called only after a successful apply requiring restart
	Shutdown       func() // called after daemon accepts a full-stack shutdown

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
	case tray.IntentReservations:
		c.startReservations(ctx)
	case tray.IntentCreateRepository:
		c.startCreateRepository(ctx, intent.ServerID)
	case tray.IntentPairMobileDevice:
		c.startPairMobileDevice(ctx, intent.ServerID)
	case tray.IntentUpdatePlan:
		c.startUpdate(ctx, false)
	case tray.IntentUpdateApply:
		c.startUpdate(ctx, true)
	case tray.IntentDetachRepository:
		c.startDetachRepository(ctx, intent.ServerID, intent.RepoID, false)
	case tray.IntentDeleteRepository:
		c.startDetachRepository(ctx, intent.ServerID, intent.RepoID, true)
	case tray.IntentRestartFileES:
		c.startStackLifecycle(ctx, true)
	case tray.IntentShutdownFileES:
		c.startStackLifecycle(ctx, false)
	}
}

func (c *Controller) startDetachRepository(ctx context.Context, serverID, repoID string, deleteRepository bool) {
	key := "detach:" + serverID + ":" + repoID
	if serverID == "" || repoID == "" || c.cfg.RepositoryDetacher == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		repo, ok := findRepo(vm, repoID)
		if !ok || repo.ServerID != serverID || !repo.Attached || !vm.Connected || vm.Stale {
			return
		}
		name := repo.DisplayName
		if strings.TrimSpace(name) == "" {
			name = repo.ID
		}
		if !deleteRepository {
			if repo.AttachmentPolicy == "required" || !vm.CanDetachRepository() {
				return
			}
			confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
				Title:       "Odłącz folder od FileES",
				Text:        fmt.Sprintf("%s\n%s\n\nSynchronizacja tego folderu zostanie zatrzymana. Pliki użytkownika pozostaną na dysku. Niewysłane dane pozostaną wyłącznie lokalnie.", name, repo.LocalPath),
				ConfirmText: "Odłącz folder", CancelText: "Anuluj",
			})
			if err != nil || !confirmed {
				return
			}
		} else {
			if repo.AttachmentPolicy == "required" || !vm.CanDeleteRepository() || !repositoryOwnedByCurrentRealm(vm, repo) {
				return
			}
			first, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
				Title:       "Odłącz trwale repozytorium",
				Text:        fmt.Sprintf("%s\n%s\n\nRepozytorium zostanie usunięte z serwera, a folder lokalny odłączony. Dane lokalne pozostaną, ale synchronizacja i historia serwerowa przestaną być dostępne.", name, repo.LocalPath),
				ConfirmText: "Przejdź dalej", CancelText: "Anuluj",
			})
			if err != nil || !first {
				return
			}
			second, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
				Title:       "Ostateczne potwierdzenie",
				Text:        "To jest operacja destrukcyjna. Serwer zastosuje skonfigurowaną retencję; w trybie panic (retencja 0 dni) nie pozostanie żadna kopia do odzyskania.\n\nCzy na pewno trwale usunąć repozytorium „" + name + "”?",
				ConfirmText: "Usuń repozytorium", CancelText: "Nie usuwaj",
			})
			if err != nil || !second {
				return
			}
		}
		latest := c.cfg.ViewModel()
		current, ok := findRepo(latest, repoID)
		if !ok || current.ServerID != serverID || !current.Attached || !latest.Connected || latest.Stale {
			return
		}
		if deleteRepository {
			if current.AttachmentPolicy == "required" || !latest.CanDeleteRepository() || !repositoryOwnedByCurrentRealm(latest, current) {
				return
			}
		} else if !latest.CanDetachRepository() || current.AttachmentPolicy == "required" {
			return
		}
		if err := c.cfg.RepositoryDetacher.DetachRepository(ctx, serverID, repoID, deleteRepository); err != nil {
			if ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: "repository-detach." + repoID, Group: "repository-detach." + repoID, Title: "Nie udało się odłączyć repozytorium", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
			return
		}
		title := "Folder odłączony od FileES"
		if deleteRepository {
			title = "Repozytorium trwale odłączone"
		}
		c.notify(ctx, platform.Notification{ID: "repository-detach." + repoID, Group: "repository-detach." + repoID, Title: title, Body: name, Urgency: platform.UrgencyNormal})
	}()
}

func repositoryOwnedByCurrentRealm(vm app.ViewModel, repo app.RepoViewModel) bool {
	for _, server := range vm.Servers {
		if server.ID == repo.ServerID {
			return server.Owns(repo) && server.CanOfferRepositoryCreation()
		}
	}
	return false
}

func (c *Controller) startStackLifecycle(ctx context.Context, restart bool) {
	key := "stack-lifecycle"
	if c.cfg.Stack == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.Connected || vm.Stale || (restart && !vm.CanRestartFileES()) || (!restart && !vm.CanShutdownFileES()) {
			return
		}
		request := platform.ConfirmRequest{
			Title:       "Uruchom FileES ponownie",
			Text:        "Daemon kontrolowanie zakończy bieżące operacje i opróżni kolejkę zmian, po czym daemon i GUI uruchomią się ponownie.",
			ConfirmText: "Uruchom ponownie", CancelText: "Anuluj",
		}
		if !restart {
			request = platform.ConfirmRequest{
				Title:       "Zamknij FileES",
				Text:        "Synchronizacja zostanie zatrzymana, a daemon i GUI zamknięte. Zmiany wykonane później zostaną wykryte przy następnym uruchomieniu FileES.",
				ConfirmText: "Zamknij FileES", CancelText: "Anuluj",
			}
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, request)
		if err != nil || !confirmed {
			return
		}
		if restart && c.cfg.PrepareRestart != nil {
			c.cfg.PrepareRestart()
		}
		if restart {
			err = c.cfg.Stack.RestartFileES(ctx)
		} else {
			err = c.cfg.Stack.ShutdownFileES(ctx)
		}
		if err != nil {
			if restart && c.cfg.AbortRestart != nil {
				c.cfg.AbortRestart()
			}
			if ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: "stack-lifecycle", Group: "stack-lifecycle", Title: "Nie udało się zmienić stanu FileES", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
			return
		}
		if restart && c.cfg.Restart != nil {
			c.cfg.Restart()
		}
		if !restart && c.cfg.Shutdown != nil {
			c.cfg.Shutdown()
		}
	}()
}

func (c *Controller) startCreateRepository(ctx context.Context, serverID string) {
	key := "create-repository:" + serverID
	if serverID == "" || c.cfg.FolderPicker == nil || c.cfg.Prompter == nil || c.cfg.RepositoryCreator == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		operationHeld := true
		defer func() {
			if operationHeld {
				c.endOperation(key)
			}
		}()
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
		// The mutating request has returned a durable operation ID. Release the
		// short UI de-duplication gate before the potentially long monitor loop;
		// the daemon lifecycle store, not this in-memory GUI mutex, owns overlap
		// and retry safety from this point onward.
		c.endOperation(key)
		operationHeld = false
		c.awaitCreationOutcome(ctx, serverID, displayName, operationID)
	}()
}

// startPairMobileDevice fetches a pairing token via MobilePairer and hands
// off to the separate pairing-helper process. Unlike repository creation,
// there is no daemon-side lifecycle to poll afterward: the helper process
// itself owns the rest of the user-facing flow (PIN gate, QR rendering,
// success/expiry), so this only reports whether the helper could be
// launched at all.
func (c *Controller) startPairMobileDevice(ctx context.Context, serverID string) {
	key := "pair-mobile:" + serverID
	if serverID == "" || c.cfg.MobilePairer == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.Connected || vm.Stale {
			return
		}
		if err := c.cfg.MobilePairer.Launch(ctx, serverID); err != nil {
			if ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: "pair-mobile." + serverID, Group: "pair-mobile." + serverID, Title: "Nie można sparować urządzenia mobilnego", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
		}
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
	pollCtx, cancelPoll := context.WithDeadline(ctx, deadline)
	defer cancelPoll()
	delay := interval
	var lastStatusError error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			body := displayName + " — nie udało się potwierdzić końcowego wyniku operacji " + operationID
			if lastStatusError != nil {
				body += ": " + lastStatusError.Error()
			}
			c.notify(ctx, platform.Notification{
				ID: "repository-create." + serverID, Group: "repository-create." + serverID,
				Title: "Status tworzenia repozytorium jest nieznany", Body: body,
				Urgency: platform.UrgencyCritical,
			})
			return
		}
		if delay > remaining {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		state, lastError, err := c.cfg.RepositoryCreator.CreationStatus(pollCtx, operationID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			lastStatusError = err
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			continue
		}
		lastStatusError = nil
		delay = interval
		switch state {
		case "error":
			body := displayName
			if strings.TrimSpace(lastError) != "" {
				body = displayName + " — " + lastError
			}
			c.notify(ctx, platform.Notification{ID: "repository-create." + serverID, Group: "repository-create." + serverID, Title: "Nie udało się utworzyć repozytorium", Body: body, Urgency: platform.UrgencyCritical})
			return
		case "repository_created":
			if strings.TrimSpace(lastError) != "" {
				c.notify(ctx, platform.Notification{
					ID: "repository-create." + serverID, Group: "repository-create." + serverID,
					Title:   "Nie udało się dokończyć tworzenia repozytorium",
					Body:    displayName + " — " + lastError + ". Ponowienie użyje już utworzonego repozytorium.",
					Urgency: platform.UrgencyCritical,
				})
				return
			}
		case "attached":
			c.notify(ctx, platform.Notification{ID: "repository-create." + serverID, Group: "repository-create." + serverID, Title: "Repozytorium utworzone", Body: displayName, Urgency: platform.UrgencyNormal})
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
			if c.cfg.PrepareRestart != nil {
				c.cfg.PrepareRestart()
			}
			if c.cfg.Stack != nil {
				if err := c.cfg.Stack.RestartFileES(ctx); err != nil {
					if c.cfg.AbortRestart != nil {
						c.cfg.AbortRestart()
					}
					c.updateFailure(ctx, "Aktualizacja została zainstalowana, ale restart FileES nie powiódł się", err)
					return
				}
			}
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
		c.offerLocalPinSetup(ctx)
		c.notify(ctx, platform.Notification{ID: "activation", Group: "activation", Title: "Klient FileES aktywowany na serwerze", Body: endpoint.Value, Urgency: platform.UrgencyNormal})
	}()
}

// offerLocalPinSetup prompts for a local PIN once, right after a successful
// activation, if none is configured yet. Best-effort and silent on
// cancel/failure - skipping here does not block activation from
// succeeding; the mandatory PIN gate at mobile-pairing launch time offers
// setup again if the user skipped it here.
func (c *Controller) offerLocalPinSetup(ctx context.Context) {
	if c.cfg.PinStore == nil || c.cfg.Prompter == nil {
		return
	}
	if configured, err := c.cfg.PinStore.IsConfigured(); err != nil || configured {
		return
	}
	prompted, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Ustaw PIN do generowania kodu parowania telefonu (opcjonalnie):", Secret: true})
	if err != nil || prompted.Cancelled || prompted.Value == "" {
		return
	}
	pin := []byte(prompted.Value)
	defer clear(pin)
	_ = c.cfg.PinStore.Setup(pin)
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

func (c *Controller) startReservations(ctx context.Context) {
	if c.cfg.Reservations == nil || c.cfg.ReservationBrowser == nil || c.cfg.Prompter == nil || !c.beginOperation("reservations") {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("reservations")
		c.handleReservations(ctx)
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
	if !lock && repo.ReservationCount == 0 {
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
	if c.cfg.Refresh != nil {
		c.cfg.Refresh()
	}
}

type reservationEntry struct {
	serverID    string
	serverName  string
	reservation app.Reservation
}

func (c *Controller) handleReservations(ctx context.Context) {
	for {
		vm := c.cfg.ViewModel()
		if !vm.CanBrowseReservations() {
			return
		}
		entries := make([]reservationEntry, 0)
		for _, server := range vm.Servers {
			reservations, err := c.cfg.Reservations.ListReservations(ctx, server.ID)
			if err != nil {
				if ctx.Err() == nil {
					c.notify(ctx, platform.Notification{ID: "reservations", Group: "reservations", Title: "Nie można pobrać rezerwacji", Body: err.Error(), Urgency: platform.UrgencyNormal})
				}
				return
			}
			serverName := server.DisplayName
			if strings.TrimSpace(serverName) == "" {
				serverName = server.ID
			}
			for _, reservation := range reservations {
				entries = append(entries, reservationEntry{serverID: server.ID, serverName: serverName, reservation: reservation})
			}
		}
		if len(entries) == 0 {
			return
		}
		rows, byID := reservationRows(entries)
		result, err := c.cfg.ReservationBrowser.ShowReservations(ctx, platform.ReservationDialogRequest{
			Title: "Lista rezerwacji plikowych",
			Text:  "Aktywne rezerwacje widoczne z lokalnych folderów roboczych. Kolumna „Serwer” rozdziela pozycje między aktywacjami FileES.",
			Rows:  rows,
		})
		if err != nil || ctx.Err() != nil {
			if err != nil && ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: "reservations", Group: "reservations", Title: "Błąd listy rezerwacji", Body: err.Error(), Urgency: platform.UrgencyNormal})
			}
			return
		}
		switch result.Action {
		case platform.ReservationDialogClose:
			return
		case platform.ReservationDialogRefresh:
			continue
		case platform.ReservationDialogRelease:
			entry, ok := byID[result.RowID]
			if !ok {
				continue
			}
			reservation := entry.reservation
			if !reservation.CanRelease {
				_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Rezerwacja niedostępna", Text: "Ta rezerwacja jest powiązana z aktywnym paszportem edycji na innym urządzeniu i nie może zostać zwolniona z tego klienta."})
				continue
			}
			risk := reservation.LocalChanges || reservation.ActivePassport
			text := fmt.Sprintf("%s\nFolder roboczy: %s\n\nZwolnienie odbierze blokadę SVN innym osobom.", reservation.Path, reservation.WorkingCopy)
			if risk {
				text += "\n\nTen folder ma lokalne zmiany lub aktywny paszport edycji. Otwarte programy mogą mieć niezapisane dane; FileES nie bada uchwytów otwartych przez edytory. Kontynuować świadomie?"
			}
			confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Zwolnij rezerwację", Text: text, ConfirmText: "Zwolnij", CancelText: "Anuluj"})
			if err != nil || !confirmed || ctx.Err() != nil {
				continue
			}
			vm = c.cfg.ViewModel()
			if !vm.CanReleaseReservations() || !viewHasServer(vm, entry.serverID) {
				return
			}
			err = c.cfg.Reservations.ReleaseReservation(ctx, app.ReservationReleaseRequest{ServerID: entry.serverID, RepoID: reservation.RepoID, Path: reservation.Path, ExpectedToken: reservation.Token, ConfirmRisk: risk})
			if err != nil {
				if ctx.Err() == nil {
					c.notify(ctx, platform.Notification{ID: "release_reservation." + entry.serverID, Group: "release_reservation." + entry.serverID, Title: "Nie można zwolnić rezerwacji", Body: err.Error(), Urgency: platform.UrgencyNormal})
				}
				continue
			}
			c.notify(ctx, platform.Notification{ID: "release_reservation." + entry.serverID, Group: "release_reservation." + entry.serverID, Title: "Zwolniono rezerwację", Body: reservation.Path, Urgency: platform.UrgencyLow})
			if c.cfg.Refresh != nil {
				c.cfg.Refresh()
			}
		}
	}
}

func reservationRows(entries []reservationEntry) ([]platform.ReservationDialogRow, map[string]reservationEntry) {
	rows := make([]platform.ReservationDialogRow, 0, len(entries))
	byID := make(map[string]reservationEntry, len(entries))
	for i, entry := range entries {
		reservation := entry.reservation
		id := fmt.Sprintf("reservation-%d", i)
		status := "dostępne"
		if !reservation.CanRelease {
			status = "niedostępne na tym urządzeniu"
		} else if reservation.LocalChanges || reservation.ActivePassport {
			status = "wymaga potwierdzenia"
		}
		rows = append(rows, platform.ReservationDialogRow{ID: id, Server: entry.serverName, WorkingCopy: reservation.WorkingCopy, Path: reservation.Path, Owner: reservation.Owner, CreatedAt: reservation.CreatedAt, ReleaseStatus: status})
		byID[id] = entry
	}
	return rows, byID
}

func viewHasServer(vm app.ViewModel, serverID string) bool {
	for _, server := range vm.Servers {
		if server.ID == serverID {
			return true
		}
	}
	return false
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
