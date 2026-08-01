// Package actions dispatches tray intents to platform services and the daemon.
// It imports tray (for Intent types), app (for ViewModel and DaemonClient shape),
// and platform — never ipcclient or engine packages.
package actions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
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

type RealmAliasManager interface {
	ClaimAlias(context.Context, string, string) error
}

type Activator interface {
	Begin(ctx context.Context, invitation string) (ActivationTarget, error)
	Finish(ctx context.Context, target ActivationTarget, otp []byte) error
}

// ActivationTarget is derived from a validated invitation, never typed as a
// transport endpoint by the user.
type ActivationTarget struct {
	ServerID string
	Address  string
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

// RepositoryDumpLoader triggers LOAD_REPOSITORY_DUMP for a repository whose
// only history so far is the carrier commit the user just made
// (LOAD_REPOSITORY_DUMP_CONCEPT.md). No options are exposed here yet - the
// first pass always applies the shared ignore policy and keeps the full
// history.
type RepositoryDumpLoader interface {
	LoadDump(ctx context.Context, serverID, repoID string) error
}

type ServerDetacher interface {
	DetachServer(context.Context, string) error
}

type RealmGrantRecipient struct {
	RealmID string
	Alias   string
}

type RealmGrantManager interface {
	ListRecipients(context.Context, string) ([]RealmGrantRecipient, error)
	SetVisibility(context.Context, string, string) error
	Grant(context.Context, string, string, string, string) error
	Revoke(context.Context, string, string, string) error
}

type RealmRemovalBeginRequest struct {
	ServerID, NotificationEmail, RecoveryDirectory string
	ErasureRequested                               bool
}
type RealmRemovalBeginResult struct {
	OperationID, RecoveryKitPath, ExpiresAt                    string
	ActiveClientCount, OwnedRepositoryCount, ForeignGrantCount int
}
type RealmRemovalConfirmResult struct {
	RecoveryKitPath, DownloadUntil, AdminGraceUntil string
	ArchiveCount                                    int
	ErasureRequested                                bool
	ErasureMaxDays                                  int
}
type RealmRemover interface {
	BeginRealmRemoval(context.Context, RealmRemovalBeginRequest) (RealmRemovalBeginResult, error)
	ConfirmRealmRemoval(context.Context, string, string, []byte, string) (RealmRemovalConfirmResult, error)
}
type RecoveryDownloader interface {
	DownloadRecovery(context.Context, string, string) ([]string, error)
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
	Intents              <-chan tray.Intent
	ViewModel            func() app.ViewModel
	Opener               platform.FolderOpener
	Picker               platform.FilePicker
	FolderPicker         platform.FolderPicker
	Prompter             platform.Prompter
	RepositoryCreator    RepositoryCreator
	RepositoryDetacher   RepositoryDetacher
	RepositoryDumpLoader RepositoryDumpLoader
	ServerDetacher       ServerDetacher
	RealmRemover         RealmRemover
	RecoveryDownloader   RecoveryDownloader
	MobilePairer         MobilePairingLauncher
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
	RealmAliases       RealmAliasManager
	RealmGrants        RealmGrantManager
	ReservationBrowser platform.ReservationBrowser
	SettingsBrowser    platform.SettingsBrowser
	RealmGrantBrowser  platform.RealmGrantBrowser
	ConsentPrompter    platform.ConsentPrompter
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
	case tray.IntentSetRealmAlias:
		c.startRealmAlias(ctx, intent.ServerID)
	case tray.IntentServerInfo:
		c.startServerInfo(ctx, intent.ServerID)
	case tray.IntentSettings:
		c.startSettings(ctx, intent.ServerID)
	case tray.IntentRecoveries:
		c.startRecoverySettings(ctx)
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
	case tray.IntentDetachServer:
		c.startDetachServer(ctx, intent.ServerID)
	case tray.IntentRestartFileES:
		c.startStackLifecycle(ctx, true)
	case tray.IntentShutdownFileES:
		c.startStackLifecycle(ctx, false)
	}
}

func (c *Controller) startSettings(ctx context.Context, serverID string) {
	if serverID == "" {
		return
	}
	request, ok := settingsDialogRequest(c.cfg.ViewModel(), serverID)
	if !ok {
		return
	}
	c.showSettings(ctx, "settings:"+serverID, request)
}

// startRecoverySettings is intentionally a narrow global dialog, rather than
// a second instance-wide settings surface: recovery archives may outlive the
// activation that created them and therefore have no server submenu.
func (c *Controller) startRecoverySettings(ctx context.Context) {
	request, ok := recoverySettingsDialogRequest(c.cfg.ViewModel())
	if !ok {
		return
	}
	c.showSettings(ctx, "recoveries", request)
}

func (c *Controller) showSettings(ctx context.Context, operationKey string, request platform.SettingsDialogRequest) {
	if c.cfg.SettingsBrowser == nil || !c.beginOperation(operationKey) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(operationKey)
		result, err := c.cfg.SettingsBrowser.ShowSettings(ctx, request)
		if err != nil && ctx.Err() == nil {
			c.notify(ctx, platform.Notification{ID: operationKey, Group: operationKey, Title: "Nie udało się otworzyć okna FileES", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		switch result.Action {
		case platform.SettingsDialogAddFolder:
			c.startCreateRepository(ctx, result.ServerID)
		case platform.SettingsDialogDetachFolder:
			c.startDetachRepository(ctx, result.ServerID, result.RepoID, false)
		case platform.SettingsDialogDeleteRepo:
			c.startDetachRepository(ctx, result.ServerID, result.RepoID, true)
		case platform.SettingsDialogLoadDump:
			c.startLoadDump(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogManageGrants:
			c.startManageRealmGrants(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogRealmVisibility:
			c.startSetRealmVisibility(ctx, result.ServerID)
		case platform.SettingsDialogDetachServer:
			c.startDetachServer(ctx, result.ServerID)
		case platform.SettingsDialogRemoveRealm:
			c.startRealmRemoval(ctx, result.ServerID)
		case platform.SettingsDialogDownloadRecovery:
			c.startRecoveryDownload(ctx, result.OperationID)
		}
	}()
}

func (c *Controller) startRecoveryDownload(ctx context.Context, operationID string) {
	key := "recovery-download:" + operationID
	if operationID == "" || c.cfg.RecoveryDownloader == nil || c.cfg.FolderPicker == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		var recovery *app.RecoveryViewModel
		for i := range vm.Recoveries {
			item := &vm.Recoveries[i]
			if item.OperationID == operationID {
				recovery = item
				break
			}
		}
		if recovery == nil || !recovery.CanDownload {
			return
		}
		folder, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: "Wybierz katalog dla archiwów repozytoriów"})
		if err != nil || folder.Cancelled || !filepath.IsAbs(folder.Path) {
			return
		}
		paths, err := c.cfg.RecoveryDownloader.DownloadRecovery(ctx, operationID, filepath.Clean(folder.Path))
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się pobrać archiwów", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Archiwa repozytoriów pobrane", Text: strings.Join(paths, "\n")})
	}()
}

func (c *Controller) startRealmRemoval(ctx context.Context, serverID string) {
	key := "remove-realm:" + serverID
	if serverID == "" || c.cfg.RealmRemover == nil || c.cfg.Prompter == nil || c.cfg.ConsentPrompter == nil || c.cfg.FolderPicker == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		warning := "Ta operacja usunie z serwera repozytoria należące do Twojego realmu, cofnie granty i unieważni aktywacje wszystkich Twoich klientów — nie tylko tej instalacji. Lokalne pliki pozostaną na dysku."
		ok, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Usuń mój udział FileES", Text: warning, ConfirmText: "Rozumiem, kontynuuj", CancelText: "Anuluj"})
		if err != nil || !ok {
			return
		}
		consent, err := c.cfg.ConsentPrompter.ConfirmConsent(ctx, platform.ConsentRequest{
			Title:        "Retencja i usunięcie danych",
			Text:         "Usunięcie udziału nie oznacza natychmiastowego usunięcia danych z kopii zapasowych i logów bezpieczeństwa.",
			RequiredText: "Rozumiem, że dane mogą pozostać w backupach zgodnie z polityką retencji serwera.",
			OptionalText: "Dodatkowo składam żądanie usunięcia wszystkich moich danych.",
		})
		if err != nil || consent.Cancelled || !consent.Required {
			return
		}
		email, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Adres do powiadomień", Text: "Kod OTP i powiadomienia o usunięciu danych zostaną wysłane na ten adres.", Placeholder: "email@example.com"})
		if err != nil || email.Cancelled || strings.TrimSpace(email.Value) == "" {
			return
		}
		directory, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: "Wybierz katalog dla pakietu odzyskiwania .fkr"})
		if err != nil || directory.Cancelled || !filepath.IsAbs(directory.Path) {
			return
		}
		begin, err := c.cfg.RealmRemover.BeginRealmRemoval(ctx, RealmRemovalBeginRequest{
			ServerID: serverID, NotificationEmail: strings.TrimSpace(email.Value),
			RecoveryDirectory: filepath.Clean(directory.Path), ErasureRequested: consent.Optional,
		})
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się rozpocząć usuwania udziału", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		otpText := fmt.Sprintf("Kod wysłano e-mailem. Potwierdzenie usunie %d repozytoriów, cofnie %d grantów i unieważni %d aktywacji klientów. Jeśli to nie Ty rozpocząłeś operację, zignoruj wiadomość i skontaktuj się z administratorem serwera.", begin.OwnedRepositoryCount, begin.ForeignGrantCount, begin.ActiveClientCount)
		otp, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Potwierdź usunięcie udziału kodem OTP", Text: otpText, Placeholder: "Kod OTP", Secret: true})
		if err != nil || otp.Cancelled || strings.TrimSpace(otp.Value) == "" {
			return
		}
		secret := []byte(strings.TrimSpace(otp.Value))
		result, err := c.cfg.RealmRemover.ConfirmRealmRemoval(ctx, serverID, begin.OperationID, secret, begin.RecoveryKitPath)
		for i := range secret {
			secret[i] = 0
		}
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się dokończyć usuwania udziału", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		info := "Udział FileES został usunięty."
		if result.ArchiveCount > 0 {
			info += fmt.Sprintf("\n\nPakiet odzyskiwania zapisano w:\n%s\n\nArchiwa: %d. Pobieranie jest dostępne do %s; potem do %s pozostaje kontakt z administratorem.", result.RecoveryKitPath, result.ArchiveCount, result.DownloadUntil, result.AdminGraceUntil)
		} else {
			info += "\n\nSerwer nie zachował archiwów repozytoriów (retencja wynosi 0 albo udział nie zawierał własnych repozytoriów). Nie utworzono akcji odzyskiwania."
		}
		if result.ErasureRequested {
			info += fmt.Sprintf("\n\nŻądanie usunięcia wszystkich danych zostało przyjęte. Proces może potrwać do %d dni; o zakończeniu zostaniesz poinformowany e-mailem.", result.ErasureMaxDays)
		}
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Usuwanie udziału przyjęte", Text: info})
		if c.cfg.Refresh != nil {
			c.cfg.Refresh()
		}
	}()
}

func settingsDialogRequest(vm app.ViewModel, serverID string) (platform.SettingsDialogRequest, bool) {
	for _, server := range vm.Servers {
		if server.ID != serverID {
			continue
		}
		name := server.DisplayName
		if strings.TrimSpace(name) == "" {
			name = server.ID
		}
		address := server.Address
		if address == "" {
			address = "brak danych"
		} else {
			address = settingsServerHost(address)
		}
		realm := server.RealmAlias
		if realm == "" {
			realm = "alias nieustawiony"
		}
		clientID := server.ClientID
		if clientID == "" {
			clientID = "brak danych"
		}
		request := platform.SettingsDialogRequest{Title: "FileES — " + name, Text: "Informacje o serwerze, aktywacji i lokalnych folderach."}
		row := platform.SettingsServer{ID: server.ID, Name: name, Address: address, Realm: realm, ClientID: clientID, CanSetRealmVisibility: vm.CanSetRealmVisibility()}
		for _, repo := range server.Repos {
			// Optional remote projections are not folders on this client. Showing
			// them beside their attached counterpart produces duplicate-looking
			// rows and contradicts the server-popup contract. Required entries
			// remain visible so a server-mandated attachment is not hidden.
			if !repo.Attached && repo.AttachmentPolicy != "required" {
				continue
			}
			repoName := repo.DisplayName
			if strings.TrimSpace(repoName) == "" {
				repoName = repo.ID
			}
			path := repo.LocalPath
			if path == "" {
				path = "brak lokalnego folderu"
			}
			state := settingsRepositoryState(repo)
			access := "tylko odczyt"
			if repo.Access == "rw" {
				access = "odczyt i zapis"
			}
			row.Folders = append(row.Folders, platform.SettingsFolder{ID: repo.ID, Name: repoName, LocalPath: path, State: state, Access: access, CanManageGrants: vm.CanManageRealmGrants() && server.Owns(repo) && server.CanOfferRepositoryCreation()})
		}
		request.Servers = append(request.Servers, row)
		return request, true
	}
	return platform.SettingsDialogRequest{}, false
}

// settingsServerHost keeps the management table readable. The transport port
// remains available in technical server information, but is not part of the
// user-facing server label. It also avoids duplicating a port that is already
// present in older profile addresses.
func settingsServerHost(address string) string {
	address = strings.TrimSpace(address)
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	if colon := strings.LastIndex(address, ":"); colon > 0 && strings.Count(address, ":") == 1 {
		if _, err := strconv.ParseUint(address[colon+1:], 10, 16); err == nil {
			return address[:colon]
		}
	}
	return address
}

func recoverySettingsDialogRequest(vm app.ViewModel) (platform.SettingsDialogRequest, bool) {
	if len(vm.Recoveries) == 0 {
		return platform.SettingsDialogRequest{}, false
	}
	request := platform.SettingsDialogRequest{Title: "Odzyskiwanie repozytoriów FileES", Text: "Dostępne archiwa odzyskiwania."}
	for _, recovery := range vm.Recoveries {
		status := "Pobierz archiwa repozytoriów do " + recovery.DownloadUntil
		if !recovery.CanDownload {
			contact := recovery.AdminContact
			if contact == "" {
				contact = "administrator serwera"
			}
			status = "Odzyskiwanie repozytoriów: " + contact + " do " + recovery.AdminGraceUntil
		}
		request.Recoveries = append(request.Recoveries, platform.SettingsRecovery{
			OperationID: recovery.OperationID, ServerName: recovery.ServerName, KitPath: recovery.KitPath,
			Status: status, CanDownload: recovery.CanDownload,
		})
	}
	return request, true
}

func settingsRepositoryState(repo app.RepoViewModel) string {
	if !repo.Attached {
		return "nieprzypięte lokalnie"
	}
	switch repo.DisplayState() {
	case app.RepoDisplayActive:
		return "aktywne"
	case app.RepoDisplayBusy, app.RepoDisplayInitializing, app.RepoDisplayBaselining:
		return "praca w toku"
	case app.RepoDisplayPaused:
		return "wstrzymane"
	case app.RepoDisplayStopping:
		return "zatrzymywanie"
	case app.RepoDisplayOffline:
		return "offline"
	case app.RepoDisplayAttention:
		return "wymaga uwagi"
	case app.RepoDisplayDisabled:
		return "wyłączone"
	case app.RepoDisplayRevoked:
		return "dostęp cofnięty"
	default:
		return "stan nieznany"
	}
}

func (c *Controller) startDetachServer(ctx context.Context, serverID string) {
	key := "detach-server:" + serverID
	if serverID == "" || c.cfg.ServerDetacher == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.CanDetachServer() {
			return
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Odłącz serwer od FileES", Text: "Wszystkie lokalne foldery tego serwera zostaną odłączone; pliki pozostaną na dysku. Serwer unieważni klucz tej instalacji, a lokalny profil z credentialami zostanie usunięty.", ConfirmText: "Odłącz serwer", CancelText: "Anuluj"})
		if err != nil || !confirmed {
			return
		}
		if err := c.cfg.ServerDetacher.DetachServer(ctx, serverID); err != nil {
			if ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: "server-detach." + serverID, Group: "server-detach." + serverID, Title: "Nie udało się odłączyć serwera", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
			return
		}
		c.notify(ctx, platform.Notification{ID: "server-detach." + serverID, Group: "server-detach." + serverID, Title: "Serwer odłączony od FileES", Body: "Lokalne dane pozostały na dysku", Urgency: platform.UrgencyNormal})
		if c.cfg.Refresh != nil {
			c.cfg.Refresh()
		}
	}()
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

// startLoadDump triggers LOAD_REPOSITORY_DUMP for repoID. First pass: no
// options dialog - always applies the shared ignore policy and keeps the
// full history (RepositoryDumpLoader.LoadDump takes no parameters yet). The
// daemon does the actual swap asynchronously (localrepo.StateReconciling);
// this call only starts it, the same fire-and-forget shape as detach.
func (c *Controller) startLoadDump(ctx context.Context, serverID, repoID string) {
	key := "load-dump:" + serverID + ":" + repoID
	if serverID == "" || repoID == "" || c.cfg.RepositoryDumpLoader == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		repo, ok := findRepo(vm, repoID)
		if !ok || repo.ServerID != serverID || !repo.Attached || !vm.Connected || vm.Stale || !repositoryOwnedByCurrentRealm(vm, repo) {
			return
		}
		name := repo.DisplayName
		if strings.TrimSpace(name) == "" {
			name = repo.ID
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
			Title:       "Odtwórz z archiwum",
			Text:        fmt.Sprintf("%s\n%s\n\nZawartość tego folderu zostanie zastąpiona danymi z wcześniej skopiowanego tam archiwum. Bieżąca zawartość jest odkładana na bok na czas operacji i usuwana dopiero po potwierdzonym sukcesie.", name, repo.LocalPath),
			ConfirmText: "Odtwórz z archiwum", CancelText: "Anuluj",
		})
		if err != nil || !confirmed {
			return
		}
		latest := c.cfg.ViewModel()
		current, ok := findRepo(latest, repoID)
		if !ok || current.ServerID != serverID || !current.Attached || !latest.Connected || latest.Stale || !repositoryOwnedByCurrentRealm(latest, current) {
			return
		}
		if err := c.cfg.RepositoryDumpLoader.LoadDump(ctx, serverID, repoID); err != nil {
			if ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: "repository-load-dump." + repoID, Group: "repository-load-dump." + repoID, Title: "Nie udało się rozpocząć odtwarzania z archiwum", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
			return
		}
		c.notify(ctx, platform.Notification{ID: "repository-load-dump." + repoID, Group: "repository-load-dump." + repoID, Title: "Odtwarzanie z archiwum rozpoczęte", Body: name, Urgency: platform.UrgencyNormal})
	}()
}

func (c *Controller) startManageRealmGrants(ctx context.Context, serverID, repoID string) {
	key := "realm-grants." + serverID + "." + repoID
	if serverID == "" || repoID == "" || c.cfg.RealmGrants == nil || c.cfg.RealmGrantBrowser == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.CanManageRealmGrants() {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Zarządzanie dostępem jest niedostępne", Body: "Demon FileES nie udostępnia kompletnej obsługi grantów.", Urgency: platform.UrgencyCritical})
			return
		}
		var (
			server app.ServerViewModel
			repo   app.RepoViewModel
			found  bool
		)
		for _, candidate := range vm.Servers {
			if candidate.ID != serverID {
				continue
			}
			server = candidate
			for _, candidateRepo := range candidate.Repos {
				if candidateRepo.ID == repoID {
					repo, found = candidateRepo, true
					break
				}
			}
			break
		}
		if !found || !server.Owns(repo) || !server.CanOfferRepositoryCreation() {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie można zarządzać dostępem", Body: "Granty może zmieniać wyłącznie właściciel repozytorium.", Urgency: platform.UrgencyCritical})
			return
		}
		recipients, err := c.cfg.RealmGrants.ListRecipients(ctx, serverID)
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się pobrać realmów", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		if len(recipients) == 0 {
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Dostęp do „" + repo.DisplayName + "”", Text: "Brak widocznych realmów. Odbiorca musi najpierw włączyć widoczność w prywatnym katalogu realmów."})
			return
		}
		request := platform.RealmGrantDialogRequest{Title: "Dostęp do „" + repo.DisplayName + "”", Text: "Wybierz realm i docelowy poziom dostępu. Ponowne nadanie zmienia istniejący grant."}
		known := make(map[string]RealmGrantRecipient, len(recipients))
		for _, recipient := range recipients {
			known[recipient.RealmID] = recipient
			request.Recipients = append(request.Recipients, platform.RealmGrantRecipient{RealmID: recipient.RealmID, Alias: recipient.Alias})
		}
		choice, err := c.cfg.RealmGrantBrowser.ShowRealmGrants(ctx, request)
		if err != nil || choice.Action == platform.RealmGrantDialogClose {
			if err != nil && ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się otworzyć grantów", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
			return
		}
		recipient, ok := known[choice.RealmID]
		if !ok {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nieprawidłowy odbiorca", Body: "Wybrany realm nie pochodzi z aktualnego katalogu odbiorców.", Urgency: platform.UrgencyCritical})
			return
		}
		label := strings.TrimSpace(recipient.Alias)
		if label == "" {
			label = recipient.RealmID
		}
		actionText := "nadać dostęp tylko do odczytu"
		if choice.Action == platform.RealmGrantDialogWrite {
			actionText = "nadać dostęp do odczytu i zapisu"
		} else if choice.Action == platform.RealmGrantDialogRevoke {
			actionText = "cofnąć dostęp"
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Potwierdź zmianę dostępu", Text: "Czy " + actionText + " realmowi „" + label + "” do repozytorium „" + repo.DisplayName + "”?", ConfirmText: "Zastosuj", CancelText: "Anuluj"})
		if err != nil || !confirmed {
			return
		}
		if choice.Action == platform.RealmGrantDialogRevoke {
			err = c.cfg.RealmGrants.Revoke(ctx, serverID, repoID, recipient.RealmID)
		} else {
			access := "r"
			if choice.Action == platform.RealmGrantDialogWrite {
				access = "rw"
			}
			err = c.cfg.RealmGrants.Grant(ctx, serverID, repoID, recipient.RealmID, access)
		}
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się zmienić dostępu", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Dostęp został zaktualizowany", Body: repo.DisplayName + " — " + label, Urgency: platform.UrgencyNormal})
	}()
}

func (c *Controller) startSetRealmVisibility(ctx context.Context, serverID string) {
	key := "realm-visibility." + serverID
	if serverID == "" || c.cfg.RealmGrants == nil || c.cfg.RealmGrantBrowser == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.CanSetRealmVisibility() {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Widoczność realmu jest niedostępna", Body: "Demon FileES nie obsługuje katalogu realmów.", Urgency: platform.UrgencyCritical})
			return
		}
		var server app.ServerViewModel
		found := false
		for _, candidate := range vm.Servers {
			if candidate.ID == serverID {
				server, found = candidate, true
				break
			}
		}
		if !found || strings.TrimSpace(server.RealmID) == "" || strings.TrimSpace(server.RealmAlias) == "" {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie można zmienić widoczności", Body: "Najpierw ustaw stały alias realmu.", Urgency: platform.UrgencyCritical})
			return
		}
		choice, err := c.cfg.RealmGrantBrowser.ShowRealmVisibility(ctx, platform.RealmVisibilityDialogRequest{Title: "Widoczność realmu „" + server.RealmAlias + "”", Text: "Widoczny realm może zostać wybrany jako odbiorca grantu. Nie ujawnia to repozytoriów ani istniejących dostępów. Tak — widoczny; Nie — ukryty; Anuluj — bez zmian."})
		if err != nil || choice.Action == platform.RealmVisibilityDialogClose {
			if err != nil && ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się otworzyć widoczności realmu", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
			return
		}
		if choice.Action != platform.RealmVisibilityDialogListed && choice.Action != platform.RealmVisibilityDialogPrivate {
			return
		}
		visibility := string(choice.Action)
		description := "ukryć realm w katalogu odbiorców"
		if choice.Action == platform.RealmVisibilityDialogListed {
			description = "pokazać realm w prywatnym katalogu odbiorców"
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Potwierdź widoczność realmu", Text: "Czy " + description + "?", ConfirmText: "Zastosuj", CancelText: "Anuluj"})
		if err != nil || !confirmed {
			return
		}
		if err := c.cfg.RealmGrants.SetVisibility(ctx, serverID, visibility); err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się zmienić widoczności", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		body := "Realm jest teraz ukryty."
		if choice.Action == platform.RealmVisibilityDialogListed {
			body = "Realm jest teraz widoczny dla innych aktywnych realmów."
		}
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Widoczność realmu została zmieniona", Body: body, Urgency: platform.UrgencyNormal})
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
		invitation, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Wklej zaproszenie FileES otrzymane e-mailem:", Placeholder: "filees-invite:v1:…", Secret: true})
		if err != nil || invitation.Cancelled || invitation.Value == "" {
			c.activationFailure(ctx, err)
			return
		}
		target, err := c.cfg.Activator.Begin(ctx, invitation.Value)
		if err != nil {
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
		if err := c.cfg.Activator.Finish(ctx, target, secret); err != nil {
			c.activationFailure(ctx, err)
			return
		}
		aliasPending := c.cfg.RealmAliases != nil && !c.claimRealmAlias(ctx, target.ServerID)
		if aliasPending {
			// Activation itself has already completed.  An alias is required
			// before some collaboration actions, but an interrupted alias claim
			// must never make a successfully activated client appear to vanish.
			c.notify(ctx, platform.Notification{ID: "realm_alias." + target.ServerID, Group: "realm_alias." + target.ServerID, Title: "Klient aktywowany — alias wymaga ustawienia", Body: "Ustaw stały alias z menu serwera, zanim użyjesz blokad lub współdzielonych operacji.", Urgency: platform.UrgencyNormal})
			if c.cfg.Prompter != nil {
				_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Klient FileES aktywowany", Text: "Połączenie z serwerem " + target.Address + " jest aktywne. Alias nie został jeszcze potwierdzony — ustaw go ponownie z menu serwera przed użyciem blokad lub operacji współdzielonych."})
			}
		}
		c.offerLocalPinSetup(ctx)
		body := target.Address
		if aliasPending {
			body += "\nAktywacja zakończona; alias wymaga ustawienia."
		}
		c.notify(ctx, platform.Notification{ID: "activation", Group: "activation", Title: "Klient FileES aktywowany na serwerze", Body: body, Urgency: platform.UrgencyNormal})
	}()
}

func (c *Controller) startRealmAlias(ctx context.Context, serverID string) {
	if serverID == "" || c.cfg.Prompter == nil || c.cfg.RealmAliases == nil || !c.beginOperation("realm-alias:"+serverID) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("realm-alias:" + serverID)
		vm := c.cfg.ViewModel()
		for _, server := range vm.Servers {
			if server.ID == serverID && server.RealmAlias == "" {
				c.claimRealmAlias(ctx, serverID)
				return
			}
		}
	}()
}

func (c *Controller) claimRealmAlias(ctx context.Context, serverID string) bool {
	if c.cfg.Prompter == nil || c.cfg.RealmAliases == nil {
		return false
	}
	for {
		alias, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{
			Title: "Alias FileES", Text: "Wybierz stały alias widoczny przy blokadach i przyszłych operacjach między użytkownikami.", Placeholder: "np. acme-k",
		})
		if err != nil || alias.Cancelled || strings.TrimSpace(alias.Value) == "" {
			return false
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
			Title: "Potwierdź stały alias", Text: "Alias „" + alias.Value + "” zostanie przypisany do tego realm na stałe. Nie można go później zmienić.", ConfirmText: "Ustaw alias", CancelText: "Anuluj",
		})
		if err != nil || !confirmed {
			return false
		}
		if err := c.cfg.RealmAliases.ClaimAlias(ctx, serverID, alias.Value); err == nil {
			if c.cfg.Refresh != nil {
				c.cfg.Refresh()
			}
			return true
		}
		retry, retryErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
			Title: "Alias nie został potwierdzony", Text: "Serwer nie potwierdził ustawienia aliasu. Wprowadź alias ponownie; ten sam alias można bezpiecznie ponowić po przerwanym połączeniu.", ConfirmText: "Wprowadź ponownie", CancelText: "Później",
		})
		if retryErr != nil || !retry {
			c.notify(ctx, platform.Notification{ID: "realm_alias." + serverID, Group: "realm_alias." + serverID, Title: "Alias nie został ustawiony", Body: "Klient pozostaje aktywny. Ustaw stały alias z menu serwera przed operacjami współdzielonymi.", Urgency: platform.UrgencyNormal})
			return false
		}
	}
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
		Body:    lockNotificationPaths(repo.LocalPath, paths),
		Urgency: platform.UrgencyLow,
	})
	if c.cfg.Refresh != nil {
		c.cfg.Refresh()
	}
}

func lockNotificationPaths(workingCopy string, paths []string) string {
	displayed := make([]string, 0, len(paths))
	for _, path := range paths {
		relative, err := filepath.Rel(filepath.Clean(workingCopy), filepath.Clean(path))
		if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			displayed = append(displayed, "/"+filepath.ToSlash(relative))
			continue
		}
		displayed = append(displayed, filepath.Base(filepath.Clean(path)))
	}
	return strings.Join(displayed, "\n")
}

type reservationEntry struct {
	serverID         string
	serverName       string
	workingCopyAlias string
	reservation      app.Reservation
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
				entries = append(entries, reservationEntry{serverID: server.ID, serverName: serverName, workingCopyAlias: reservationWorkingCopyAlias(server, reservation), reservation: reservation})
			}
		}
		if len(entries) == 0 {
			return
		}
		rows, byID := reservationRows(entries)
		result, err := c.cfg.ReservationBrowser.ShowReservations(ctx, platform.ReservationDialogRequest{
			Title: "Lista rezerwacji plikowych",
			Text:  "Aktywne blokady plików.",
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
		case platform.ReservationDialogReleaseAll:
			c.releaseAllReservations(ctx, entries)
			continue
		case platform.ReservationDialogRelease:
			entry, ok := byID[result.RowID]
			if !ok {
				continue
			}
			reservation := entry.reservation
			if !reservation.CanRelease {
				_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Poproś o zwolnienie", Text: "Ta blokada należy do innego użytkownika lub jest aktywna na innym urządzeniu. Wysłanie prośby o zwolnienie będzie dostępne w kolejnej wersji."})
				continue
			}
			risk := reservation.LocalChanges || reservation.ActivePassport
			text := fmt.Sprintf("%s\nKopia robocza: %s\n\nZwolnienie odbierze blokadę SVN innym osobom.", reservationDisplayPath(reservation.WorkingCopy, reservation.Path), entry.workingCopyAlias)
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
			c.notify(ctx, platform.Notification{ID: "release_reservation." + entry.serverID, Group: "release_reservation." + entry.serverID, Title: "Zwolniono rezerwację", Body: reservationDisplayPath(reservation.WorkingCopy, reservation.Path), Urgency: platform.UrgencyLow})
			if c.cfg.Refresh != nil {
				c.cfg.Refresh()
			}
		}
	}
}

func (c *Controller) releaseAllReservations(ctx context.Context, entries []reservationEntry) {
	eligible := make([]reservationEntry, 0, len(entries))
	risky := 0
	for _, entry := range entries {
		if !entry.reservation.CanRelease {
			continue
		}
		eligible = append(eligible, entry)
		if entry.reservation.LocalChanges || entry.reservation.ActivePassport {
			risky++
		}
	}
	if len(eligible) == 0 {
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Brak rezerwacji do zwolnienia", Text: "Nie ma tutaj rezerwacji należących do tego klienta. Dla cudzych blokad przygotowujemy opcję „Poproś o zwolnienie”."})
		return
	}
	text := fmt.Sprintf("Zwolnić wszystkie moje rezerwacje (%d)?\n\nCudze blokady nie zostaną zmienione.", len(eligible))
	if risky > 0 {
		text += fmt.Sprintf("\n\n%d rezerwacji jest powiązanych z lokalnymi zmianami lub aktywnym paszportem edycji. Otwarte programy mogą mieć niezapisane dane.", risky)
	}
	confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Zwolnij wszystkie moje rezerwacje", Text: text, ConfirmText: "Zwolnij wszystko", CancelText: "Anuluj"})
	if err != nil || !confirmed || ctx.Err() != nil {
		return
	}
	vm := c.cfg.ViewModel()
	if !vm.CanReleaseReservations() {
		return
	}
	released, failed := 0, 0
	for _, entry := range eligible {
		if !viewHasServer(vm, entry.serverID) {
			failed++
			continue
		}
		reservation := entry.reservation
		risk := reservation.LocalChanges || reservation.ActivePassport
		if err := c.cfg.Reservations.ReleaseReservation(ctx, app.ReservationReleaseRequest{ServerID: entry.serverID, RepoID: reservation.RepoID, Path: reservation.Path, ExpectedToken: reservation.Token, ConfirmRisk: risk}); err != nil {
			failed++
			continue
		}
		released++
	}
	if released > 0 {
		body := fmt.Sprintf("Zwolniono %d rezerwacji.", released)
		if failed > 0 {
			body += fmt.Sprintf(" Nie zwolniono: %d.", failed)
		}
		c.notify(ctx, platform.Notification{ID: "release_all_reservations", Group: "release_all_reservations", Title: "Zwolniono moje rezerwacje", Body: body, Urgency: platform.UrgencyLow})
		if c.cfg.Refresh != nil {
			c.cfg.Refresh()
		}
		return
	}
	c.notify(ctx, platform.Notification{ID: "release_all_reservations", Group: "release_all_reservations", Title: "Nie zwolniono rezerwacji", Body: "Lista zostanie odświeżona przed następną próbą.", Urgency: platform.UrgencyNormal})
}

func reservationRows(entries []reservationEntry) ([]platform.ReservationDialogRow, map[string]reservationEntry) {
	rows := make([]platform.ReservationDialogRow, 0, len(entries))
	byID := make(map[string]reservationEntry, len(entries))
	for i, entry := range entries {
		reservation := entry.reservation
		id := fmt.Sprintf("reservation-%d", i)
		action := "Zwolnij"
		if !reservation.CanRelease {
			action = "Poproś o zwolnienie (wkrótce)"
		}
		owner := reservation.OwnerLabel
		if owner == "" {
			owner = "właściciel nieustawiony"
		}
		rows = append(rows, platform.ReservationDialogRow{ID: id, Server: entry.serverName, WorkingCopy: entry.workingCopyAlias, Path: reservationDisplayPath(reservation.WorkingCopy, reservation.Path), Owner: owner, CreatedAt: formatReservationTime(reservation.CreatedAt), Action: action})
		byID[id] = entry
	}
	return rows, byID
}

func formatReservationTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return parsed.Local().Format("15:04 02-01-2006")
}

func reservationWorkingCopyAlias(server app.ServerViewModel, reservation app.Reservation) string {
	for _, repo := range server.Repos {
		if repo.ID == reservation.RepoID && strings.TrimSpace(repo.DisplayName) != "" {
			return repo.DisplayName
		}
	}
	name := filepath.Base(filepath.Clean(reservation.WorkingCopy))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "kopia robocza"
	}
	return name
}

func reservationDisplayPath(workingCopy, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if filepath.IsAbs(path) {
		if relative, err := filepath.Rel(filepath.Clean(workingCopy), filepath.Clean(path)); err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			path = relative
		}
	}
	path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	path = strings.TrimPrefix(path, "./")
	if path == "." || path == "" || strings.HasPrefix(path, "../") || path == ".." {
		return "/"
	}
	return "/" + strings.TrimPrefix(path, "/")
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
	case "realm.alias_required":
		return "Przed blokowaniem plików ustaw stały alias realm"
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
