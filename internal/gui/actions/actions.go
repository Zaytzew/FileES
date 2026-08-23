// Package actions dispatches tray intents to platform services and the daemon.
// It imports tray (for Intent types), app (for ViewModel and DaemonClient shape),
// and platform — never ipcclient or engine packages.
package actions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filees/internal/gui/app"
	"filees/internal/gui/journal"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	"filees/pkg/errcat"
	"filees/pkg/localpin"
	"filees/pkg/realmbranding"
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
	// repositorySettleInterval paces the wait that keeps a progress window on
	// screen until the tray would stop showing the busy clock. It is shorter
	// than the lifecycle poll because it only reads an in-memory ViewModel.
	repositorySettleInterval = time.Second
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
	Pending(ctx context.Context) ([]ActivationTarget, error)
	Begin(ctx context.Context, invitation string) (ActivationTarget, error)
	Finish(ctx context.Context, target ActivationTarget, otp []byte) error
	Resume(ctx context.Context, target ActivationTarget) error
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

type RepositoryAttacher interface {
	AttachRepository(ctx context.Context, serverID, repoID, localPath string) (operationID string, err error)
	AttachmentStatus(ctx context.Context, operationID string) (state, lastError string, err error)
}

type RepositoryLocator interface {
	LocateRepository(ctx context.Context, serverID, repoID, existingLocalPath string) (operationID string, err error)
	// LocateStatus observes the durable outcome. A rejected locate returns to
	// "attached" with LastError set rather than entering lifecycle "error".
	LocateStatus(ctx context.Context, operationID string) (state, lastError string, err error)
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
	Access  string
	State   string
}

type RealmGrantManager interface {
	ListRecipients(context.Context, string, string) ([]RealmGrantRecipient, error)
	SetVisibility(context.Context, string, string) error
	Grant(context.Context, string, string, string, string) error
	Revoke(context.Context, string, string, string) error
	// SetEditingPolicy switches one owned repository between plain
	// merge-on-commit and edit passports. It takes a bool rather than a policy
	// string so the GUI layer never has to know the wire vocabulary; the
	// adapter translates. Returns the policy the server actually stored.
	SetEditingPolicy(ctx context.Context, serverID, repoID string, lockRequired bool) (bool, error)
}

type RealmBrandingManager interface {
	PublicBranding(context.Context, string) (realmbranding.Branding, error)
	SetPublicBranding(context.Context, string, realmbranding.Branding) (realmbranding.Branding, error)
}

type SessionTimeoutManager interface {
	SetSessionTimeout(context.Context, string, int) (int, error)
}

type PublicShareObject struct {
	PublicID, RepoPath, DisplayName string
	Size                            *int64
}

type PublicShareSummary struct {
	ChannelID, Alias, Slug, State, SourceRoot, UpdatedAt string
	Recipients                                           []string
	PasswordProtected                                    bool
	DoNotFollow                                          *int64
	Objects                                              []PublicShareObject
}

type PublicShareDeclaration struct {
	RepoID, SourceRoot, Slug string
	Recipients               []string
	Password                 []byte
	KeepPassword             bool
	DoNotFollow              *int64
	Objects                  []PublicShareObject
}

type PublicShareManager interface {
	ListPublicShares(context.Context, string, string) ([]PublicShareSummary, error)
	CreatePublicShare(context.Context, string, PublicShareDeclaration) error
	UpdatePublicShare(context.Context, string, string, PublicShareDeclaration) error
	RevokePublicShare(context.Context, string, string, string) error
	DeletePublicShare(context.Context, string, string, string) error
}

type UploadChannelSummary struct {
	ChannelID, Alias, Slug, State, UploadRepoID, UpdatedAt string
	Recipients                                             []string
}

type UploadChannelDeclaration struct {
	AuthorityRepoID, Slug string
	Recipients            []string
}

type UploadChannelManager interface {
	ListUploadChannels(context.Context, string, string) ([]UploadChannelSummary, error)
	CreateUploadChannel(context.Context, string, UploadChannelDeclaration) error
	UpdateUploadChannel(context.Context, string, string, UploadChannelDeclaration) error
	RevokeUploadChannel(context.Context, string, string, string) error
	DeleteUploadChannel(context.Context, string, string, string) error
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

type ShoutPublisher interface {
	Publish(ctx context.Context, repoID, comment string) (int64, error)
}

type NoticeAcker interface {
	AckNotice(ctx context.Context, noticeID string) error
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
	// PresentationDetails carries the structured fields belonging to the
	// message key. Only fields a known key defines may be read; anything else
	// is diagnostic text, not something to show a user.
	PresentationDetails() map[string]string
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
	RepositoryAttacher   RepositoryAttacher
	RepositoryLocator    RepositoryLocator
	RepositoryDetacher   RepositoryDetacher
	RepositoryDumpLoader RepositoryDumpLoader
	ServerDetacher       ServerDetacher
	RealmRemover         RealmRemover
	RecoveryDownloader   RecoveryDownloader
	MobilePairer         MobilePairingLauncher
	// PinStore, if non-nil, offers PIN setup at the end of a successful
	// activation (see startActivation) - nil means the local-PIN feature is
	// disabled entirely (e.g. platform without a durable state root).
	PinStore             *localpin.Store
	Activator            Activator
	Updater              Updater
	Stack                StackLifecycle
	Notifier             platform.Notifier // nil → notifications silently dropped
	Locker               LockUnlocker
	Reservations         ReservationManager
	RealmAliases         RealmAliasManager
	RealmGrants          RealmGrantManager
	RealmBranding        RealmBrandingManager
	SessionTimeouts      SessionTimeoutManager
	PublicShares         PublicShareManager
	UploadChannels       UploadChannelManager
	Shouts               ShoutPublisher
	Notices              NoticeAcker
	ReservationBrowser   platform.ReservationBrowser
	SettingsBrowser      platform.SettingsBrowser
	JournalBrowser       platform.JournalBrowser
	RealmGrantBrowser    platform.RealmGrantBrowser
	PublicShareBrowser   platform.PublicShareBrowser
	UploadChannelBrowser platform.UploadChannelBrowser
	ConsentPrompter      platform.ConsentPrompter
	// Progress renders the "still working" window for operations that keep
	// running after their dialog closes. nil → the window is simply skipped;
	// the operation itself is unaffected, because this surface never decides
	// anything.
	Progress  platform.ProgressPresenter
	Reconnect func() // nil → reconnect intent is a no-op
	// Refresh obtains a fresh daemon snapshot without reconnecting. It is used
	// after a successful mutation whose result changes tray eligibility.
	Refresh func()
	// ActionLifecycle starts renderer badges with a mutation and keeps them alive
	// until a subsequent full daemon snapshot projects the expected effect. Nil
	// preserves the legacy fire-and-refresh behaviour used without badges.
	ActionLifecycle interface {
		StartAction(app.PendingAction) bool
		AwaitActionProjection(string)
		FinishAction(string)
	}
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
	pendingMu    sync.Mutex
	pending      map[string]pendingAttachment
	actionSeq    atomic.Uint64
	tasks        sync.WaitGroup
}

type pendingAttachment struct {
	localPath   string
	operationID string
}

// New creates a Controller with the given configuration.
func New(cfg Config) *Controller {
	return &Controller{cfg: cfg, operations: make(map[string]struct{}), pending: make(map[string]pendingAttachment)}
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
		c.startLockUnlock(ctx, intent.RepoID, true, intent.ActionID)
	case tray.IntentUnlock:
		c.startLockUnlock(ctx, intent.RepoID, false, intent.ActionID)
	case tray.IntentReleaseReservation:
		c.startReservationRelease(ctx, intent.ReservationID, intent.ActionID)
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
		c.startSettings(ctx, intent.ServerID, intent.RepoID)
	case tray.IntentJournal:
		c.startJournal(ctx)
	case tray.IntentRecoveries:
		c.startRecoverySettings(ctx)
	case tray.IntentDownloadRecovery:
		c.startRecoveryDownload(ctx, intent.RecoveryOperationID)
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
	case tray.IntentPublish:
		c.startPublish(ctx, intent.RepoID)
	case tray.IntentAckNotice:
		c.startAckNotice(ctx, intent.NoticeID)
	case tray.IntentLocateFolder:
		c.startLocateRepository(ctx, intent.ServerID, intent.RepoID)
	}
}

func (c *Controller) startJournal(ctx context.Context) {
	vm := c.cfg.ViewModel()
	if c.cfg.JournalBrowser == nil || (!vm.CanListActivity() && !vm.CanListErrors()) || !c.beginOperation("journal") {
		return
	}
	entries := journal.Build(vm)
	rows := make([]platform.JournalDialogRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, platform.JournalDialogRow{
			Timestamp:  entry.ExactTime,
			Repository: entry.Repo,
			Summary:    entry.Summary,
			Details:    entry.Details,
			Severity:   entry.Severity,
			Emphasized: entry.Emphasized,
		})
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("journal")
		err := c.cfg.JournalBrowser.ShowJournal(ctx, platform.JournalDialogRequest{
			Title: "Dziennik FileES",
			Text:  "Aktywność i błędy ze wszystkich lokalnych repozytoriów, od najnowszych.",
			Rows:  rows,
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			c.notify(ctx, platform.Notification{ID: "journal", Group: "journal", Title: "Nie udało się otworzyć dziennika", Body: err.Error(), Urgency: platform.UrgencyCritical})
		}
	}()
}

func (c *Controller) startSettings(ctx context.Context, serverID, repoID string) {
	vm := c.cfg.ViewModel()
	request, ok := c.settingsDialogRequest(vm, serverID, repoID)
	if !ok {
		return
	}
	key := "settings"
	if serverID != "" {
		key = "settings:" + serverID
	}
	if repoID != "" {
		key += ":" + repoID
	}
	c.showSettings(ctx, key, request)
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
		case platform.SettingsDialogConnectRepos:
			// The native settings window closes before folder selection. Release
			// its de-duplication key now so the connect flow can reopen the
			// refreshed repository list after the last picker.
			c.endOperation(operationKey)
			c.startConnectRepositories(ctx, result.ServerID, result.RepoIDs, request.FocusRepoID == "")
		case platform.SettingsDialogLocateFolder:
			c.endOperation(operationKey)
			c.startLocateRepository(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogDetachFolder:
			c.startDetachRepository(ctx, result.ServerID, result.RepoID, false)
		case platform.SettingsDialogDeleteRepo:
			c.startDetachRepository(ctx, result.ServerID, result.RepoID, true)
		case platform.SettingsDialogLoadDump:
			c.startLoadDump(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogManageGrants:
			c.startManageRealmGrants(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogEditingPolicy:
			c.startSetEditingPolicy(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogPublicShares:
			c.startManagePublicShares(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogUploadChannels:
			c.startManageUploadChannels(ctx, result.ServerID, result.RepoID)
		case platform.SettingsDialogRealmVisibility:
			c.startSetRealmVisibility(ctx, result.ServerID)
		case platform.SettingsDialogRealmBranding:
			c.startSetRealmBranding(ctx, result.ServerID)
		case platform.SettingsDialogSessionTimeout:
			c.startSetSessionTimeout(ctx, result.ServerID)
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
		if recovery != nil && !recovery.CanDownload {
			c.reportActionError(ctx, key, "Pobieranie archiwów jest niedostępne", "Okno samodzielnego pobrania już minęło. Został kontakt z administratorem serwera.")
			return
		}
		folder, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: "Wybierz katalog dla archiwów repozytoriów"})
		if err != nil {
			c.reportActionError(ctx, key, "Nie udało się wybrać katalogu archiwów", actionErrorBody(err))
			return
		}
		if folder.Cancelled {
			return
		}
		if !filepath.IsAbs(folder.Path) {
			c.reportActionError(ctx, key, "Nie udało się pobrać archiwów", "Wybrana ścieżka nie jest bezwzględna")
			return
		}
		paths, err := c.cfg.RecoveryDownloader.DownloadRecovery(ctx, operationID, filepath.Clean(folder.Path))
		if err != nil {
			c.reportActionError(ctx, key, "Nie udało się pobrać archiwów", actionErrorBody(err))
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
		warning := "Ta operacja usunie z serwera repozytoria należące do Twojej strefy, cofnie granty i unieważni aktywacje wszystkich Twoich klientów — nie tylko tej instalacji. Lokalne pliki pozostaną na dysku."
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
			c.reportActionError(ctx, key, "Nie udało się rozpocząć usuwania udziału", actionErrorBody(err))
			return
		}
		otpText := fmt.Sprintf("Kod wysłano e-mailem. Potwierdzenie usunie %d repozytoriów, cofnie %d grantów i unieważni %d aktywacji klientów. Przygotowanie dumpów może potrwać — nie zamykaj FileES. Jeśli to nie Ty rozpocząłeś operację, zignoruj wiadomość i skontaktuj się z administratorem serwera.", begin.OwnedRepositoryCount, begin.ForeignGrantCount, begin.ActiveClientCount)
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
			c.reportActionError(ctx, key, "Nie udało się dokończyć usuwania udziału", actionErrorBody(err))
			return
		}
		info := "Udział FileES został usunięty."
		if result.ArchiveCount > 0 {
			info += fmt.Sprintf("\n\nPakiet odzyskiwania zapisano w:\n%s\n\nArchiwa: %d. Pobieranie jest dostępne do %s; potem do %s pozostaje kontakt z administratorem.\n\nNastępne okno pozwoli pobrać dumpy. Później ta sama lista jest w menu FileES → Odzyskiwanie repozytoriów…", result.RecoveryKitPath, result.ArchiveCount, result.DownloadUntil, result.AdminGraceUntil)
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
		if result.ArchiveCount > 0 {
			c.startRecoveryDownload(ctx, begin.OperationID)
		}
	}()
}

func (c *Controller) settingsDialogRequest(vm app.ViewModel, serverID, repoID string) (platform.SettingsDialogRequest, bool) {
	request := platform.SettingsDialogRequest{Title: "Ustawienia FileES", Text: "Wybierz serwer, potem działanie."}
	for _, server := range vm.Servers {
		if serverID != "" && server.ID != serverID {
			continue
		}
		pending := c.pendingAttachments(vm, server.ID)
		row, hadPending := settingsServerRow(vm, server, pending)
		if hadPending {
			request.Text = "Wybierz serwer, potem działanie. Pierwszy checkout trwa w tle; wiersz „łączenie…” odświeży się po potwierdzeniu przez demona."
		}
		if serverID != "" {
			request.Title = "FileES — " + row.Name
			request.Text = "Wybierz działanie dla tego serwera."
			if hadPending {
				request.Text += " Pierwszy checkout trwa w tle; wiersz „łączenie…” odświeży się po potwierdzeniu przez demona."
			}
		}
		if repoID != "" {
			var focused *platform.SettingsFolder
			for i := range row.Folders {
				if row.Folders[i].ID == repoID {
					folder := row.Folders[i]
					focused = &folder
					break
				}
			}
			if focused == nil {
				continue
			}
			row.Folders = []platform.SettingsFolder{*focused}
			request.FocusRepoID = repoID
			request.Title = "Folder — " + focused.Name
			request.Text = "Działania dotyczą wyłącznie tego folderu."
		}
		request.Servers = append(request.Servers, row)
	}
	if repoID == "" {
		if rec, ok := recoverySettingsDialogRequest(vm); ok {
			request.Recoveries = rec.Recoveries
		}
	}
	if len(request.Servers) == 0 && len(request.Recoveries) == 0 {
		return platform.SettingsDialogRequest{}, false
	}
	return request, true
}

func settingsServerRow(vm app.ViewModel, server app.ServerViewModel, pending map[string]pendingAttachment) (platform.SettingsServer, bool) {
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
	minutes := server.SessionTimeoutMin
	if minutes <= 0 {
		minutes = 30
	}
	row := platform.SettingsServer{ID: server.ID, Name: name, Address: address, Realm: realm, ClientID: clientID, CanSetRealmVisibility: vm.CanSetRealmVisibility() && strings.TrimSpace(server.RealmAlias) != "", CanSetRealmBranding: vm.CanSetRealmBranding() && strings.TrimSpace(server.RealmAlias) != "", CanSetSessionTimeout: vm.CanSetSessionTimeout(), SessionTimeoutMin: minutes, CanAddFolder: vm.Connected && !vm.Stale && server.CanOfferRepositoryCreation()}
	hadPending := false
	for _, repo := range server.Repos {
		repoName := repo.DisplayName
		if strings.TrimSpace(repoName) == "" {
			repoName = repo.ID
		}
		path := repo.LocalPath
		if path == "" {
			path = "brak lokalnego folderu"
		}
		state := settingsRepositoryState(repo)
		pendingAttachment, connecting := pending[repo.ID]
		if connecting {
			path = pendingAttachment.localPath
			state = "łączenie…"
			hadPending = true
		}
		access := "tylko odczyt"
		if repo.Access == "rw" {
			access = "odczyt i zapis"
		}
		attachmentRequired := repo.AttachmentPolicy == "required"
		ownedAndCreatable := server.Owns(repo) && server.CanOfferRepositoryCreation()
		lockRequired := repo.RequiresLock()
		// Shown on every client, not only the owner's: this is the sentence
		// that turns an unexplained read-only file into a stated rule.
		editing := "swobodna"
		if lockRequired {
			editing = "wymaga wypożyczenia"
		}
		row.Folders = append(row.Folders, platform.SettingsFolder{
			ID: repo.ID, Name: repoName, LocalPath: path, State: state, Access: access,
			Editing:      editing,
			LockRequired: lockRequired,
			// Ownership alone, not ownedAndCreatable: whether a realm may
			// create new repositories says nothing about its right to set
			// the working rules of one it already owns.
			CanSetEditingPolicy:     vm.CanSetEditingPolicy() && server.Owns(repo) && repo.Attached,
			CanManageGrants:         vm.CanManageRealmGrants() && ownedAndCreatable,
			CanManagePublicShares:   vm.CanManagePublicShares() && ownedAndCreatable,
			CanManageUploadChannels: vm.CanManageUploadChannels() && ownedAndCreatable,
			CanConnect:              !connecting && !repo.Attached && repo.DisplayState() == app.RepoDisplayUnattached && vm.CanAttachRepository(),
			CanLocate:               repo.Attached && repo.DisplayState() == app.RepoDisplayAttention && repo.CurrentOp != nil && *repo.CurrentOp == "working_copy_missing" && vm.CanLocateRepository(),
			CanDetach:               repo.Attached && !attachmentRequired && vm.CanDetachRepository(),
			CanDelete:               repo.Attached && !attachmentRequired && vm.CanDeleteRepository() && ownedAndCreatable,
			CanLoadDump:             repo.Attached && ownedAndCreatable,
		})
	}
	return row, hadPending
}

func (c *Controller) startLocateRepository(ctx context.Context, serverID, repoID string) {
	key := "locate-repository:" + serverID + ":" + repoID
	if serverID == "" || repoID == "" || c.cfg.RepositoryLocator == nil || c.cfg.FolderPicker == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		repo, ok := locatableRepository(c.cfg.ViewModel(), serverID, repoID)
		if !ok {
			return
		}
		name := strings.TrimSpace(repo.DisplayName)
		if name == "" {
			name = repo.ID
		}
		picked, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: "Wskaż przeniesioną kopię roboczą repozytorium „" + name + "”"})
		if err != nil {
			c.reportActionError(ctx, key, "Nie można wskazać kopii roboczej", name+" — "+actionErrorBody(err))
			return
		}
		if picked.Cancelled {
			return
		}
		if !filepath.IsAbs(picked.Path) {
			c.reportActionError(ctx, key, "Nie można wskazać kopii roboczej", name+" — wybrana ścieżka nie jest bezwzględna")
			return
		}
		operationID, err := c.cfg.RepositoryLocator.LocateRepository(ctx, serverID, repoID, filepath.Clean(picked.Path))
		if err != nil {
			c.reportActionError(ctx, key, "Nie można połączyć przeniesionej kopii", name+" — "+actionErrorBody(err))
			return
		}
		c.awaitLocateOutcome(ctx, key, name, operationID)
	}()
}

func locatableRepository(vm app.ViewModel, serverID, repoID string) (app.RepoViewModel, bool) {
	if !vm.CanLocateRepository() {
		return app.RepoViewModel{}, false
	}
	for _, server := range vm.Servers {
		if server.ID != serverID {
			continue
		}
		for _, repo := range server.Repos {
			if repo.ID == repoID && repo.Attached && repo.DisplayState() == app.RepoDisplayAttention && repo.CurrentOp != nil && *repo.CurrentOp == "working_copy_missing" {
				return repo, true
			}
		}
	}
	return app.RepoViewModel{}, false
}

func (c *Controller) awaitLocateOutcome(ctx context.Context, key, name, operationID string) {
	interval, timeout := c.cfg.CreationStatusPollInterval, c.cfg.CreationStatusPollTimeout
	if interval <= 0 {
		interval = creationStatusPollInterval
	}
	if timeout <= 0 {
		timeout = creationStatusPollTimeout
	}
	deadline := time.Now().Add(timeout)
	delay := interval
	var lastStatusError error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			body := name + " — FileES nie potwierdził wskazanej kopii"
			if lastStatusError != nil {
				body += ": " + lastStatusError.Error()
			}
			c.reportActionError(ctx, key, "Nie można połączyć przeniesionej kopii", body)
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
		state, lastError, err := c.cfg.RepositoryLocator.LocateStatus(ctx, operationID)
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
			c.reportActionError(ctx, key, "Nie można połączyć przeniesionej kopii", name+" — "+locateFailurePolish(lastError))
			return
		case "attached":
			if strings.TrimSpace(lastError) != "" {
				c.reportActionError(ctx, key, "Nie można połączyć przeniesionej kopii", name+" — "+locateFailurePolish(lastError))
				return
			}
			title := "Kopia robocza została wskazana"
			body := name + " — FileES używa teraz wybranego folderu."
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: title, Body: body, Urgency: platform.UrgencyNormal})
			if c.cfg.Prompter != nil {
				_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: title, Text: body})
			}
			if c.cfg.Refresh != nil {
				c.cfg.Refresh()
			}
			return
		}
	}
}

func (c *Controller) startConnectRepositories(ctx context.Context, serverID string, repoIDs []string, reopenSettings bool) {
	key := "connect-repositories:" + serverID
	if serverID == "" || len(repoIDs) == 0 || c.cfg.RepositoryAttacher == nil || c.cfg.FolderPicker == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		defer func() {
			if ctx.Err() == nil {
				if c.cfg.Refresh != nil {
					c.cfg.Refresh()
				}
				if reopenSettings {
					c.startSettings(ctx, serverID, "")
				}
			}
		}()

		seen := make(map[string]bool, len(repoIDs))
		for _, repoID := range repoIDs {
			repoID = strings.TrimSpace(repoID)
			if repoID == "" || seen[repoID] {
				continue
			}
			seen[repoID] = true
			vm := c.cfg.ViewModel()
			repo, ok := attachableRepository(vm, serverID, repoID)
			if !ok {
				continue
			}
			name := repo.DisplayName
			if strings.TrimSpace(name) == "" {
				name = repo.ID
			}
			picked, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: "Wybierz lub utwórz lokalny folder dla repozytorium „" + name + "”"})
			if err != nil {
				c.reportActionError(ctx, key, "Nie można wybrać lokalnego folderu", name+" — "+err.Error())
				return
			}
			if picked.Cancelled {
				return
			}
			if strings.TrimSpace(picked.Path) == "" || !filepath.IsAbs(picked.Path) {
				c.reportActionError(ctx, key, "Nie można połączyć repozytorium", name+" — wybrana ścieżka nie jest bezwzględna")
				return
			}
			if _, ok := attachableRepository(c.cfg.ViewModel(), serverID, repoID); !ok {
				continue
			}
			actionID := c.startProjectedAction(app.PendingAction{
				Kind: string(platform.SettingsDialogConnectRepos), ServerID: serverID, RepoID: repoID,
				Label: "Łączenie folderu", ExpectedRepoAttached: true,
			})
			operationID, err := c.cfg.RepositoryAttacher.AttachRepository(ctx, serverID, repoID, filepath.Clean(picked.Path))
			if err != nil {
				c.finishProjectedAction(actionID)
				_, body, _ := operationErrorPresentation("połączenie repozytorium", err)
				c.reportActionError(ctx, key, "Nie można połączyć repozytorium", name+" — "+body)
				continue
			}
			c.setPendingAttachment(serverID, repoID, filepath.Clean(picked.Path), operationID)
			c.awaitProjectedAction(actionID)
			c.notify(ctx, platform.Notification{ID: "repository-attach." + repoID, Group: "repository-attach." + repoID, Title: "Rozpoczęto pierwszy checkout", Body: name + " — " + filepath.Clean(picked.Path), Urgency: platform.UrgencyNormal})
			c.tasks.Add(1)
			go func(serverID, repoID, name, operationID, actionID, localPath string) {
				defer c.tasks.Done()
				defer c.showProgress(ctx, "Pierwszy checkout", name+" — trwa pobieranie…")()
				if !c.awaitAttachmentOutcome(ctx, serverID, repoID, name, operationID) {
					c.finishProjectedAction(actionID)
				}
				c.awaitRepositorySettled(ctx, localPath)
			}(serverID, repoID, name, operationID, actionID, filepath.Clean(picked.Path))
		}
	}()
}

func pendingAttachmentKey(serverID, repoID string) string {
	return serverID + "\x00" + repoID
}

func (c *Controller) setPendingAttachment(serverID, repoID, localPath, operationID string) {
	c.pendingMu.Lock()
	c.pending[pendingAttachmentKey(serverID, repoID)] = pendingAttachment{localPath: localPath, operationID: operationID}
	c.pendingMu.Unlock()
}

func (c *Controller) clearPendingAttachment(serverID, repoID, operationID string) {
	key := pendingAttachmentKey(serverID, repoID)
	c.pendingMu.Lock()
	if pending, ok := c.pending[key]; ok && (operationID == "" || pending.operationID == operationID) {
		delete(c.pending, key)
	}
	c.pendingMu.Unlock()
}

// pendingAttachments returns the controller-owned optimistic overlay for the
// settings snapshot. Lifecycle "attached" can precede the next daemon model
// tick, so success polling deliberately does not clear it. The overlay is
// removed only when the authoritative ViewModel finally reports Attached.
func (c *Controller) pendingAttachments(vm app.ViewModel, serverID string) map[string]pendingAttachment {
	attached := make(map[string]bool)
	for _, server := range vm.Servers {
		if server.ID != serverID {
			continue
		}
		for _, repo := range server.Repos {
			attached[repo.ID] = repo.Attached
		}
	}

	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	result := make(map[string]pendingAttachment)
	prefix := serverID + "\x00"
	for key, pending := range c.pending {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		repoID := strings.TrimPrefix(key, prefix)
		if attached[repoID] {
			delete(c.pending, key)
			continue
		}
		result[repoID] = pending
	}
	return result
}

func attachableRepository(vm app.ViewModel, serverID, repoID string) (app.RepoViewModel, bool) {
	if !vm.CanAttachRepository() {
		return app.RepoViewModel{}, false
	}
	for _, server := range vm.Servers {
		if server.ID != serverID {
			continue
		}
		for _, repo := range server.Repos {
			if repo.ID == repoID && !repo.Attached && repo.DisplayState() == app.RepoDisplayUnattached {
				return repo, true
			}
		}
	}
	return app.RepoViewModel{}, false
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
		if strings.TrimSpace(repo.LocalPath) != "" {
			switch repo.DisplayState() {
			case app.RepoDisplayInitializing, app.RepoDisplayBaselining, app.RepoDisplayBusy:
				return "import początkowy w toku"
			case app.RepoDisplayAttention:
				return "import początkowy wymaga uwagi"
			case app.RepoDisplayOffline:
				return "import początkowy — offline"
			default:
				return "lokalny folder oczekuje na aktywację"
			}
		}
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
		kind := string(tray.IntentDetachRepository)
		label := "Odłączanie folderu"
		if deleteRepository {
			kind = string(tray.IntentDeleteRepository)
			label = "Usuwanie repozytorium"
		}
		actionID := c.startProjectedAction(app.PendingAction{
			Kind: kind, ServerID: serverID, RepoID: repoID, Label: label,
			ExpectedRepoDetached: !deleteRepository, ExpectedRepoDeleted: deleteRepository,
		})
		if err := c.cfg.RepositoryDetacher.DetachRepository(ctx, serverID, repoID, deleteRepository); err != nil {
			c.finishProjectedAction(actionID)
			if ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: "repository-detach." + repoID, Group: "repository-detach." + repoID, Title: "Działanie wymaga dokończenia", Body: actionErrorBody(err), Urgency: platform.UrgencyCritical})
			}
			return
		}
		c.awaitProjectedAction(actionID)
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
		recipients, err := c.cfg.RealmGrants.ListRecipients(ctx, serverID, repoID)
		if err != nil {
			c.reportActionError(ctx, key, "Nie udało się pobrać stref", err.Error())
			return
		}
		if len(recipients) == 0 {
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Dostęp do „" + repo.DisplayName + "”", Text: "Brak widocznych stref. Odbiorca musi najpierw włączyć widoczność w prywatnym katalogu stref."})
			return
		}
		request := platform.RealmGrantDialogRequest{Title: "Uprawnienia gości — „" + repo.DisplayName + "”", Text: "Wybierz gościa i docelowy poziom dostępu. Aktualne uprawnienie jest widoczne w tabeli."}
		known := make(map[string]RealmGrantRecipient, len(recipients))
		for _, recipient := range recipients {
			known[recipient.RealmID] = recipient
			request.Recipients = append(request.Recipients, platform.RealmGrantRecipient{RealmID: recipient.RealmID, Alias: recipient.Alias, Access: recipient.Access, State: recipient.State})
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
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nieprawidłowy odbiorca", Body: "Wybrana strefa nie pochodzi z aktualnego katalogu odbiorców.", Urgency: platform.UrgencyCritical})
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
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Potwierdź zmianę dostępu", Text: "Czy " + actionText + " strefie „" + label + "” do repozytorium „" + repo.DisplayName + "”?", ConfirmText: "Zastosuj", CancelText: "Anuluj"})
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

func (c *Controller) startManagePublicShares(ctx context.Context, serverID, repoID string) {
	key := "public-shares." + serverID + "." + repoID
	if serverID == "" || repoID == "" || c.cfg.PublicShares == nil || c.cfg.PublicShareBrowser == nil || c.cfg.FolderPicker == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		for ctx.Err() == nil {
			vm := c.cfg.ViewModel()
			repo, ok := managedPublicShareRepository(vm, serverID, repoID)
			if !ok || !vm.CanManagePublicShares() {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Udostępnienia publiczne są niedostępne", Body: "Kanałami może zarządzać właściciel repozytorium na kliencie z pełną obsługą udostępnień.", Urgency: platform.UrgencyCritical})
				return
			}
			shares, err := c.cfg.PublicShares.ListPublicShares(ctx, serverID, repoID)
			if err != nil {
				c.reportActionError(ctx, key, "Nie udało się pobrać udostępnień", err.Error())
				return
			}
			request := platform.PublicShareDialogRequest{Title: "Udostępnienia publiczne — „" + repo.DisplayName + "”", Text: "Kanał otwarty może być chroniony hasłem; kanał zamknięty wysyła odbiorcom osobne zaproszenia i pięciominutowe kody OTP.", ServerID: serverID, RepoID: repoID, RepositoryName: repo.DisplayName}
			known := make(map[string]PublicShareSummary, len(shares))
			for _, share := range shares {
				known[share.ChannelID] = share
				password := "brak"
				if share.PasswordProtected {
					password = "ustawione"
				}
				revision := "HEAD"
				if share.DoNotFollow != nil {
					revision = "r" + strconv.FormatInt(*share.DoNotFollow, 10)
				}
				recipients := "kanał otwarty"
				if len(share.Recipients) > 0 {
					recipients = strings.Join(share.Recipients, ", ")
				}
				request.Shares = append(request.Shares, platform.PublicShareSummary{ChannelID: share.ChannelID, Address: share.Alias + "/" + share.Slug, State: publicShareStateLabel(share.State), SourceRoot: share.SourceRoot, Recipients: recipients, Password: password, Revision: revision})
			}
			choice, err := c.cfg.PublicShareBrowser.ShowPublicShares(ctx, request)
			if err != nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się otworzyć udostępnień", Body: err.Error(), Urgency: platform.UrgencyCritical})
				return
			}
			if choice.Action == platform.PublicShareDialogClose {
				return
			}
			var current *PublicShareSummary
			if choice.Action != platform.PublicShareDialogCreate {
				share, exists := known[choice.ChannelID]
				if !exists {
					c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nieprawidłowe udostępnienie", Body: "Wybrany kanał nie pochodzi z aktualnej listy.", Urgency: platform.UrgencyCritical})
					return
				}
				current = &share
			}
			switch choice.Action {
			case platform.PublicShareDialogCreate, platform.PublicShareDialogEdit:
				if current != nil && current.State != "active" {
					_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Kanał nie jest aktywny", Text: "Cofniętego kanału nie można edytować. Utwórz nowe udostępnienie pod nowym adresem."})
					continue
				}
				declaration, accepted := c.collectPublicShareDeclaration(ctx, repo, current)
				if !accepted {
					continue
				}
				if current == nil {
					err = c.cfg.PublicShares.CreatePublicShare(ctx, serverID, declaration)
				} else {
					err = c.cfg.PublicShares.UpdatePublicShare(ctx, serverID, current.ChannelID, declaration)
				}
				zeroBytes(declaration.Password)
			case platform.PublicShareDialogRevoke:
				if current.State != "active" {
					continue
				}
				confirmed, confirmErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Cofnij udostępnienie", Text: "Adres przestanie wydawać pliki, ale pozostanie zarezerwowany i widoczny w historii.", ConfirmText: "Cofnij", CancelText: "Anuluj"})
				if confirmErr != nil || !confirmed {
					continue
				}
				err = c.cfg.PublicShares.RevokePublicShare(ctx, serverID, repoID, current.ChannelID)
			case platform.PublicShareDialogDelete:
				confirmed, confirmErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Usuń udostępnienie", Text: "Polityka kanału zostanie usunięta, a jego adres pozostanie trwale zarezerwowany jako tombstone.", ConfirmText: "Usuń", CancelText: "Anuluj"})
				if confirmErr != nil || !confirmed {
					continue
				}
				err = c.cfg.PublicShares.DeletePublicShare(ctx, serverID, repoID, current.ChannelID)
			default:
				return
			}
			if err != nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się zmienić udostępnienia", Body: err.Error(), Urgency: platform.UrgencyCritical})
				continue
			}
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Udostępnienie zostało zaktualizowane", Body: repo.DisplayName, Urgency: platform.UrgencyNormal})
		}
	}()
}

func (c *Controller) startManageUploadChannels(ctx context.Context, serverID, repoID string) {
	key := "upload-channels." + serverID + "." + repoID
	if serverID == "" || repoID == "" || c.cfg.UploadChannels == nil || c.cfg.UploadChannelBrowser == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		for ctx.Err() == nil {
			vm := c.cfg.ViewModel()
			repo, ok := managedPublicShareRepository(vm, serverID, repoID)
			if !ok || !vm.CanManageUploadChannels() {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Półki przyjęcia są niedostępne", Body: "Półkę może wystawić właściciel repozytorium na kliencie z pełną obsługą przyjęcia.", Urgency: platform.UrgencyCritical})
				return
			}
			channels, err := c.cfg.UploadChannels.ListUploadChannels(ctx, serverID, repoID)
			if err != nil {
				c.reportActionError(ctx, key, "Nie udało się pobrać półek przyjęcia", err.Error())
				return
			}
			request := platform.UploadChannelDialogRequest{Title: "Półki przyjęcia — „" + repo.DisplayName + "”", Text: "Półka jest zawsze zamknięta: wnoszący dostają osobne zaproszenia i kładą plik przeglądarką. Domyślnie to zwykła półka, bez preselekcji."}
			known := make(map[string]UploadChannelSummary, len(channels))
			for _, channel := range channels {
				known[channel.ChannelID] = channel
				request.Channels = append(request.Channels, platform.UploadChannelSummary{ChannelID: channel.ChannelID, Address: channel.Alias + "/" + channel.Slug, State: publicShareStateLabel(channel.State), Recipients: strings.Join(channel.Recipients, ", ")})
			}
			choice, err := c.cfg.UploadChannelBrowser.ShowUploadChannels(ctx, request)
			if err != nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się otworzyć półek przyjęcia", Body: err.Error(), Urgency: platform.UrgencyCritical})
				return
			}
			if choice.Action == platform.UploadChannelDialogClose {
				return
			}
			var current *UploadChannelSummary
			if choice.Action != platform.UploadChannelDialogCreate {
				channel, exists := known[choice.ChannelID]
				if !exists {
					c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nieprawidłowa półka", Body: "Wybrany kanał nie pochodzi z aktualnej listy.", Urgency: platform.UrgencyCritical})
					return
				}
				current = &channel
			}
			switch choice.Action {
			case platform.UploadChannelDialogCreate, platform.UploadChannelDialogEdit:
				if current != nil && current.State != "active" {
					_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Półka nie jest aktywna", Text: "Cofniętej półki nie można edytować. Wystaw nową pod nowym adresem."})
					continue
				}
				declaration, accepted := c.collectUploadChannelDeclaration(ctx, repo, current)
				if !accepted {
					continue
				}
				if current == nil {
					err = c.cfg.UploadChannels.CreateUploadChannel(ctx, serverID, declaration)
				} else {
					err = c.cfg.UploadChannels.UpdateUploadChannel(ctx, serverID, current.ChannelID, declaration)
				}
			case platform.UploadChannelDialogRevoke:
				if current.State != "active" {
					continue
				}
				confirmed, confirmErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Cofnij półkę", Text: "Adres przestanie przyjmować pliki, ale pozostanie zarezerwowany. Przyjęte przesyłki zostają u Ciebie.", ConfirmText: "Cofnij", CancelText: "Anuluj"})
				if confirmErr != nil || !confirmed {
					continue
				}
				err = c.cfg.UploadChannels.RevokeUploadChannel(ctx, serverID, repoID, current.ChannelID)
			case platform.UploadChannelDialogDelete:
				confirmed, confirmErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Usuń półkę", Text: "Polityka półki zostanie usunięta, a jej adres pozostanie trwale zarezerwowany. Folder z przyjętymi plikami nie jest kasowany.", ConfirmText: "Usuń", CancelText: "Anuluj"})
				if confirmErr != nil || !confirmed {
					continue
				}
				err = c.cfg.UploadChannels.DeleteUploadChannel(ctx, serverID, repoID, current.ChannelID)
			default:
				return
			}
			if err != nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się zmienić półki", Body: err.Error(), Urgency: platform.UrgencyCritical})
				continue
			}
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Półka została zaktualizowana", Body: repo.DisplayName, Urgency: platform.UrgencyNormal})
		}
	}()
}

func (c *Controller) collectUploadChannelDeclaration(ctx context.Context, repo app.RepoViewModel, current *UploadChannelSummary) (UploadChannelDeclaration, bool) {
	declaration := UploadChannelDeclaration{AuthorityRepoID: repo.ID}
	if current == nil {
		slug, promptErr := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Adres półki", Text: "Wpisz końcówkę publicznego adresu: 3–64 małe litery, cyfry lub pojedyncze myślniki."})
		if promptErr != nil || slug.Cancelled || strings.TrimSpace(slug.Value) == "" {
			return UploadChannelDeclaration{}, false
		}
		declaration.Slug = strings.ToLower(strings.TrimSpace(slug.Value))
	} else {
		declaration.Slug = current.Slug
	}
	recipientDefault := ""
	if current != nil {
		recipientDefault = strings.Join(current.Recipients, ", ")
	}
	recipients, promptErr := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Wnoszący", Text: "Adresy e-mail oddziel przecinkiem lub średnikiem. Półka nie bywa anonimowa — lista nie może być pusta.", Default: recipientDefault})
	if promptErr != nil || recipients.Cancelled {
		return UploadChannelDeclaration{}, false
	}
	declaration.Recipients = splitRecipients(recipients.Value)
	if len(declaration.Recipients) == 0 {
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Potrzeba wnoszącego", Text: "Półka przyjęcia wymaga co najmniej jednego adresu. Anonimowe wniesienie nie istnieje."})
		return UploadChannelDeclaration{}, false
	}
	return declaration, true
}

func managedPublicShareRepository(vm app.ViewModel, serverID, repoID string) (app.RepoViewModel, bool) {
	for _, server := range vm.Servers {
		if server.ID != serverID || !server.CanOfferRepositoryCreation() {
			continue
		}
		for _, repo := range server.Repos {
			if repo.ID == repoID && server.Owns(repo) {
				return repo, true
			}
		}
	}
	return app.RepoViewModel{}, false
}

func publicShareStateLabel(state string) string {
	switch state {
	case "active":
		return "aktywne"
	case "revoked":
		return "cofnięte"
	default:
		return state
	}
}

func (c *Controller) collectPublicShareDeclaration(ctx context.Context, repo app.RepoViewModel, current *PublicShareSummary) (PublicShareDeclaration, bool) {
	if !repo.Attached || !filepath.IsAbs(repo.LocalPath) {
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Najpierw połącz repozytorium", Text: "Utworzenie lub edycja udostępnienia wymaga lokalnej kopii roboczej, z której można wybrać folder źródłowy."})
		return PublicShareDeclaration{}, false
	}
	initialDir := repo.LocalPath
	if current != nil {
		initialDir = filepath.Join(repo.LocalPath, filepath.FromSlash(current.SourceRoot))
	}
	picked, err := c.cfg.FolderPicker.PickFolder(ctx, platform.PickFolderRequest{Title: "Wybierz folder udostępnienia", InitialDir: initialDir})
	if err != nil || picked.Cancelled {
		return PublicShareDeclaration{}, false
	}
	objects, sourceRoot, err := publicShareObjects(repo.LocalPath, picked.Path, current)
	if err != nil {
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Nie można udostępnić folderu", Text: err.Error()})
		return PublicShareDeclaration{}, false
	}
	declaration := PublicShareDeclaration{RepoID: repo.ID, SourceRoot: sourceRoot, Objects: objects}
	if current == nil {
		slug, promptErr := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Adres udostępnienia", Text: "Wpisz końcówkę publicznego adresu: 3–64 małe litery, cyfry lub pojedyncze myślniki."})
		if promptErr != nil || slug.Cancelled || strings.TrimSpace(slug.Value) == "" {
			return PublicShareDeclaration{}, false
		}
		declaration.Slug = strings.ToLower(strings.TrimSpace(slug.Value))
	} else {
		declaration.Slug = current.Slug
	}
	recipientDefault := ""
	if current != nil {
		recipientDefault = strings.Join(current.Recipients, ", ")
	}
	recipients, promptErr := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Odbiorcy", Text: "Opcjonalne adresy e-mail oddziel przecinkiem lub średnikiem. Puste pole tworzy kanał otwarty.", Default: recipientDefault})
	if promptErr != nil || recipients.Cancelled {
		return PublicShareDeclaration{}, false
	}
	declaration.Recipients = splitRecipients(recipients.Value)
	if len(declaration.Recipients) == 0 {
		if current != nil && current.PasswordProtected {
			keep, confirmErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Hasło udostępnienia", Text: "Czy zachować obecne hasło? Wybierz Nie, aby je zmienić albo usunąć.", ConfirmText: "Zachowaj", CancelText: "Zmień lub usuń"})
			if confirmErr != nil {
				return PublicShareDeclaration{}, false
			}
			declaration.KeepPassword = keep
		}
		if !declaration.KeepPassword {
			password, passwordErr := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Hasło udostępnienia", Text: "Opcjonalne wspólne hasło kanału otwartego. Pozostaw puste, aby nie wymagać hasła.", Secret: true})
			if passwordErr != nil || password.Cancelled {
				return PublicShareDeclaration{}, false
			}
			declaration.Password = []byte(password.Value)
		}
	}
	revisionDefault := ""
	if current != nil && current.DoNotFollow != nil {
		revisionDefault = strconv.FormatInt(*current.DoNotFollow, 10)
	}
	revision, revisionErr := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Wersja plików", Text: "Puste pole śledzi HEAD. Wpisz numer rewizji, aby zamrozić udostępnienie.", Default: revisionDefault})
	if revisionErr != nil || revision.Cancelled {
		zeroBytes(declaration.Password)
		return PublicShareDeclaration{}, false
	}
	if value := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(revision.Value), "r")); value != "" {
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || parsed < 1 {
			zeroBytes(declaration.Password)
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Nieprawidłowa rewizja", Text: "Podaj dodatni numer rewizji albo pozostaw pole puste."})
			return PublicShareDeclaration{}, false
		}
		declaration.DoNotFollow = &parsed
	}
	return declaration, true
}

func publicShareObjects(repoRoot, selected string, current *PublicShareSummary) ([]PublicShareObject, string, error) {
	repoRoot = filepath.Clean(repoRoot)
	selected = filepath.Clean(selected)
	relRoot, err := filepath.Rel(repoRoot, selected)
	if err != nil {
		return nil, "", fmt.Errorf("nie można porównać wybranego folderu z kopią roboczą: %w", err)
	}
	if relRoot == ".." || strings.HasPrefix(relRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relRoot) {
		return nil, "", errors.New("wybierz folder wewnątrz kopii roboczej")
	}
	for _, metadata := range []string{".svn", ".filees"} {
		if relRoot == metadata || strings.HasPrefix(relRoot, metadata+string(filepath.Separator)) {
			return nil, "", errors.New("folder metadanych kopii roboczej nie może być udostępniony")
		}
	}
	info, err := os.Stat(selected)
	if err != nil || !info.IsDir() {
		return nil, "", errors.New("wybrany folder nie istnieje")
	}
	knownIDs := map[string]string{}
	if current != nil {
		for _, object := range current.Objects {
			knownIDs[object.RepoPath] = object.PublicID
		}
	}
	objects := make([]PublicShareObject, 0)
	err = filepath.WalkDir(selected, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".svn" || entry.Name() == ".filees") {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("łącza symboliczne nie mogą należeć do udostępnienia: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		repoPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		displayName, relErr := filepath.Rel(selected, path)
		if relErr != nil {
			return relErr
		}
		repoPath, displayName = filepath.ToSlash(repoPath), filepath.ToSlash(displayName)
		size := info.Size()
		objects = append(objects, PublicShareObject{PublicID: knownIDs[repoPath], RepoPath: repoPath, DisplayName: displayName, Size: &size})
		if len(objects) > 4096 {
			return errors.New("folder zawiera więcej niż 4096 plików")
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return objects, filepath.ToSlash(relRoot), nil
}

func splitRecipients(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' })
	result := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		key := strings.ToLower(field)
		if field != "" && !seen[key] {
			seen[key] = true
			result = append(result, field)
		}
	}
	return result
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
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
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Widoczność strefy jest niedostępna", Body: "Demon FileES nie obsługuje katalogu stref.", Urgency: platform.UrgencyCritical})
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
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Tożsamość strefy nie jest jeszcze dostępna", Text: "FileES nie otrzymał aliasu istniejącej strefy z serwera. Odświeżenie projekcji jest wymagane przed zmianą widoczności; nie ustawiaj nowego aliasu dla tej strefy."})
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie można zmienić widoczności", Body: "Serwer nie przekazał tożsamości istniejącej strefy; wymagane jest odświeżenie projekcji.", Urgency: platform.UrgencyCritical})
			return
		}
		choice, err := c.cfg.RealmGrantBrowser.ShowRealmVisibility(ctx, platform.RealmVisibilityDialogRequest{Title: "Widoczność strefy „" + server.RealmAlias + "”", Text: "Widoczna strefa może zostać wybrana jako odbiorca grantu. Nie ujawnia to repozytoriów ani istniejących dostępów. Tak — widoczna; Nie — ukryta; Anuluj — bez zmian."})
		if err != nil || choice.Action == platform.RealmVisibilityDialogClose {
			if err != nil && ctx.Err() == nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się otworzyć widoczności strefy", Body: err.Error(), Urgency: platform.UrgencyCritical})
			}
			return
		}
		if choice.Action != platform.RealmVisibilityDialogListed && choice.Action != platform.RealmVisibilityDialogPrivate {
			return
		}
		visibility := string(choice.Action)
		description := "ukryć strefę w katalogu odbiorców"
		if choice.Action == platform.RealmVisibilityDialogListed {
			description = "pokazać strefę w prywatnym katalogu odbiorców"
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Potwierdź widoczność strefy", Text: "Czy " + description + "?", ConfirmText: "Zastosuj", CancelText: "Anuluj"})
		if err != nil || !confirmed {
			return
		}
		if err := c.cfg.RealmGrants.SetVisibility(ctx, serverID, visibility); err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się zmienić widoczności", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		body := "Strefa jest teraz ukryta."
		if choice.Action == platform.RealmVisibilityDialogListed {
			body = "Strefa jest teraz widoczna dla innych aktywnych stref."
		}
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Widoczność strefy została zmieniona", Body: body, Urgency: platform.UrgencyNormal})
	}()
}

func (c *Controller) startSetSessionTimeout(ctx context.Context, serverID string) {
	key := "session-timeout." + serverID
	if serverID == "" || c.cfg.SessionTimeouts == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		if !c.cfg.ViewModel().CanSetSessionTimeout() {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie można teraz zmienić limitu czasu", Body: "Ta wersja FileES nie pozwala ustawić, jak długo czekać na wysyłkę i pobieranie.", Urgency: platform.UrgencyCritical})
			return
		}
		current := 30
		for _, server := range c.cfg.ViewModel().Servers {
			if server.ID == serverID && server.SessionTimeoutMin > 0 {
				current = server.SessionTimeoutMin
				break
			}
		}
		prompted, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{
			Title:   "Limit czasu wysyłki i pobierania",
			Text:    "Ile minut FileES ma czekać, aż jedno wysłanie lub pobranie na tym serwerze się skończy? Zwykle 30. Przy wolnym łączu duże pliki mogą potrzebować więcej. Od 1 do 1440.",
			Default: strconv.Itoa(current),
		})
		if err != nil || prompted.Cancelled {
			return
		}
		minutes, convErr := strconv.Atoi(strings.TrimSpace(prompted.Value))
		if convErr != nil {
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Nieprawidłowy limit", Text: "Podaj liczbę minut od 1 do 1440."})
			return
		}
		actionID := c.startProjectedAction(app.PendingAction{
			Kind:                      string(platform.SettingsDialogSessionTimeout),
			ServerID:                  serverID,
			Label:                     "Zapisywanie limitu czasu",
			ExpectedSessionTimeoutMin: minutes,
		})
		saved, setErr := c.cfg.SessionTimeouts.SetSessionTimeout(ctx, serverID, minutes)
		if setErr != nil {
			c.finishProjectedAction(actionID)
			title, body, urgency := operationErrorPresentation("limit czasu wysyłki", setErr)
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: title, Body: body, Urgency: urgency})
			return
		}
		c.awaitProjectedAction(actionID)
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Zapisano limit czasu", Body: "FileES będzie czekał do " + strconv.Itoa(saved) + " min na jedno wysłanie lub pobranie.", Urgency: platform.UrgencyNormal})
	}()
}

func (c *Controller) startSetRealmBranding(ctx context.Context, serverID string) {
	key := "realm-branding." + serverID
	if serverID == "" || c.cfg.RealmBranding == nil || c.cfg.Picker == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.CanSetRealmBranding() {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Wygląd udziałów jest niedostępny", Body: "Demon FileES nie obsługuje brandingu strefy.", Urgency: platform.UrgencyCritical})
			return
		}
		current, err := c.cfg.RealmBranding.PublicBranding(ctx, serverID)
		if err != nil {
			c.reportActionError(ctx, key, "Nie udało się pobrać wyglądu udziałów", err.Error())
			return
		}
		color, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Kolor udziałów publicznych", Text: "Podaj kolor wiodący w zapisie #RRGGBB.", Default: current.LeadingColor, Placeholder: realmbranding.DefaultLeadingColor})
		if err != nil || color.Cancelled {
			return
		}
		requested := current
		requested.LeadingColor = strings.ToUpper(strings.TrimSpace(color.Value))
		choose, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Logo udziałów publicznych", Text: "Czy wybrać nowe logo PNG lub JPEG? Logo zostanie proporcjonalnie dopasowane do pola po prawej stronie nagłówka.", ConfirmText: "Wybierz logo", CancelText: "Bez nowego logo"})
		if err != nil {
			return
		}
		if choose {
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się otworzyć wyboru logo", Body: homeErr.Error(), Urgency: platform.UrgencyCritical})
				return
			}
			picked, pickErr := c.cfg.Picker.PickFiles(ctx, platform.PickFilesRequest{Title: "Wybierz logo PNG lub JPEG", InitialDir: home, AllowOutsideRoot: true})
			if pickErr != nil {
				_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Nie udało się wybrać logo", Text: pickErr.Error()})
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się wybrać logo", Body: pickErr.Error(), Urgency: platform.UrgencyCritical})
				return
			}
			if picked.Cancelled || len(picked.Paths) == 0 {
				return
			}
			info, statErr := os.Stat(picked.Paths[0])
			if statErr != nil || info.Size() < 1 || info.Size() > realmbranding.MaxLogoInputBytes {
				message := "Wybierz plik PNG lub JPEG o rozmiarze do 16 MiB."
				if statErr != nil {
					message = statErr.Error()
				}
				_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Logo jest nieprawidłowe", Text: message})
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Logo jest nieprawidłowe", Body: message, Urgency: platform.UrgencyCritical})
				return
			}
			raw, readErr := os.ReadFile(picked.Paths[0])
			if readErr != nil {
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się odczytać logo", Body: readErr.Error(), Urgency: platform.UrgencyCritical})
				return
			}
			requested, err = realmbranding.PrepareLogo(requested.LeadingColor, http.DetectContentType(raw), raw)
			if err != nil {
				_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Logo jest nieprawidłowe", Text: err.Error()})
				c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Logo jest nieprawidłowe", Body: err.Error(), Urgency: platform.UrgencyCritical})
				return
			}
		} else if current.LogoBase64 != "" {
			remove, removeErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Obecne logo", Text: "Czy usunąć obecne logo? Wybierz Nie, aby je zachować.", ConfirmText: "Usuń logo", CancelText: "Zachowaj"})
			if removeErr != nil {
				return
			}
			if remove {
				requested.LogoMediaType, requested.LogoBase64 = "", ""
			}
		}
		requested, err = realmbranding.Normalize(requested)
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Wygląd udziałów jest nieprawidłowy", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		if _, err := c.cfg.RealmBranding.SetPublicBranding(ctx, serverID, requested); err != nil {
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Nie udało się zapisać wyglądu udziałów", Text: err.Error()})
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się zapisać wyglądu udziałów", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Wygląd udziałów został zapisany", Body: "Kolor i logo obowiązują we wszystkich publicznych udziałach tej strefy.", Urgency: platform.UrgencyNormal})
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
		prompted, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Nowe repozytorium FileES", Text: "Nazwa repozytorium:", Default: name})
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
		// The picker has closed and the import runs for tens of seconds, during
		// which the tray legitimately shows transient states. Without a window
		// the user is left staring at those and reading them as failures.
		defer c.showProgress(ctx, "Tworzenie repozytorium", displayName+" — trwa import początkowy…")()
		c.awaitCreationOutcome(ctx, serverID, displayName, operationID)
		// "attached" only means the working copy is bound; the initial import
		// keeps pushing after that. Hold the window for the rest of it.
		c.awaitRepositorySettled(ctx, picked.Path)
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

// showProgress opens the "still working" window and returns its closer. The
// returned function is always safe to call, so callers can `defer` it without
// a nil check.
//
// Every failure path here is deliberately silent. This window explains a wait;
// it never gates, decides or reports anything, so a missing zenity or a
// PowerShell that would not start must not turn a working import into a
// user-visible error.
func (c *Controller) showProgress(ctx context.Context, title, text string) func() {
	if c.cfg.Progress == nil {
		return func() {}
	}
	close, err := c.cfg.Progress.ShowProgress(ctx, platform.ProgressRequest{Title: title, Text: text})
	if err != nil || close == nil {
		return func() {}
	}
	return close
}

// awaitRepositorySettled blocks while the working copy at localPath still has
// work in flight, so a progress window closes when the *user's* wait ends.
//
// The lifecycle poll behind repository creation returns at "attached", which is
// when the daemon has bound the working copy — not when it has finished pushing
// its contents. Closing the window there put it back on screen for a fraction
// of the real wait and left the tray showing the busy clock alone, which is the
// state this window exists to explain.
//
// Busy is read exactly as the tray reads it (RepoViewModel.CurrentOp, plus the
// two transient startup states), so the window and the icon can never disagree.
// The wait is bounded: an unknown repo, a stalled daemon or a missing
// ViewModel must not pin a window on screen forever.
func (c *Controller) awaitRepositorySettled(ctx context.Context, localPath string) {
	if c.cfg.ViewModel == nil || strings.TrimSpace(localPath) == "" {
		return
	}
	timeout := c.cfg.CreationStatusPollTimeout
	if timeout <= 0 {
		timeout = creationStatusPollTimeout
	}
	target := filepath.Clean(localPath)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !c.repositoryBusy(target) {
			return
		}
		timer := time.NewTimer(repositorySettleInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// repositoryBusy reports whether the working copy at target is mid-operation.
// A repository the ViewModel does not know yet counts as busy: right after
// creation it has not necessarily appeared in a projection, and treating that
// gap as "done" would reintroduce the early close.
func (c *Controller) repositoryBusy(target string) bool {
	for _, repo := range c.cfg.ViewModel().Repos {
		if filepath.Clean(repo.LocalPath) != target {
			continue
		}
		return repo.ShowsBusy()
	}
	return true
}

func (c *Controller) awaitAttachmentOutcome(ctx context.Context, serverID, repoID, displayName, operationID string) bool {
	interval, timeout := c.cfg.CreationStatusPollInterval, c.cfg.CreationStatusPollTimeout
	if interval <= 0 {
		interval = creationStatusPollInterval
	}
	if timeout <= 0 {
		timeout = creationStatusPollTimeout
	}
	deadline := time.Now().Add(timeout)
	delay := interval
	var lastStatusError error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			body := displayName + " — nie udało się potwierdzić pierwszego checkoutu " + operationID
			if lastStatusError != nil {
				body += ": " + lastStatusError.Error()
			}
			c.notify(ctx, platform.Notification{ID: "repository-attach." + repoID, Group: "repository-attach." + repoID, Title: "Status połączenia repozytorium jest nieznany", Body: body, Urgency: platform.UrgencyCritical})
			return false
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
			return false
		case <-timer.C:
		}
		state, lastError, err := c.cfg.RepositoryAttacher.AttachmentStatus(ctx, operationID)
		if err != nil {
			if ctx.Err() != nil {
				return false
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
			c.clearPendingAttachment(serverID, repoID, operationID)
			body := displayName
			if strings.TrimSpace(lastError) != "" {
				body += " — " + lastError
			}
			c.notify(ctx, platform.Notification{ID: "repository-attach." + repoID, Group: "repository-attach." + repoID, Title: "Pierwszy checkout nie powiódł się", Body: body, Urgency: platform.UrgencyCritical})
			return false
		case "attached":
			c.notify(ctx, platform.Notification{ID: "repository-attach." + repoID, Group: "repository-attach." + repoID, Title: "Repozytorium połączone", Body: displayName, Urgency: platform.UrgencyNormal})
			if c.cfg.Refresh != nil {
				c.cfg.Refresh()
			}
			return true
		}
	}
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
		pending, err := c.cfg.Activator.Pending(ctx)
		if err != nil {
			c.activationFailure(ctx, err)
			return
		}
		for _, target := range pending {
			resume, confirmErr := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
				Title:       "Niedokończona aktywacja FileES",
				Text:        "Znaleziono niedokończoną aktywację serwera " + target.Address + ". Wznów ją bez ponownego wklejania zaproszenia?",
				ConfirmText: "Wznów", CancelText: "Inne zaproszenie",
			})
			if confirmErr != nil {
				c.activationFailure(ctx, confirmErr)
				return
			}
			if !resume {
				continue
			}
			if err := c.cfg.Activator.Resume(ctx, target); err != nil {
				// A reconnect succeeds when the OTP was already consumed. If the
				// previous GUI stopped earlier, the same durable attempt still
				// needs its OTP and can finish without importing the invitation.
				if !c.finishActivationWithOTP(ctx, target) {
					return
				}
			}
			c.activationComplete(ctx, target)
			return
		}
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
		if !c.finishActivationWithOTP(ctx, target) {
			return
		}
		c.activationComplete(ctx, target)
	}()
}

func (c *Controller) finishActivationWithOTP(ctx context.Context, target ActivationTarget) bool {
	otp, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{Title: "Aktywacja FileES", Text: "Wprowadź kod OTP otrzymany e-mailem:", Secret: true})
	if err != nil || otp.Cancelled || otp.Value == "" {
		c.activationFailure(ctx, err)
		return false
	}
	secret := []byte(otp.Value)
	defer clear(secret)
	if err := c.cfg.Activator.Finish(ctx, target, secret); err != nil {
		c.activationFailure(ctx, err)
		return false
	}
	return true
}

func (c *Controller) activationComplete(ctx context.Context, target ActivationTarget) {
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
			if server.ID == serverID && server.NeedsRealmAliasClaim() {
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
			Title: "Potwierdź stały alias", Text: "Alias „" + alias.Value + "” zostanie przypisany do tej strefy na stałe. Nie można go później zmienić.", ConfirmText: "Ustaw alias", CancelText: "Anuluj",
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
	if c.cfg.Prompter != nil {
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: "Aktywacja FileES nie powiodła się", Text: err.Error()})
	}
}

func (c *Controller) startPublish(ctx context.Context, repoID string) {
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		c.handlePublish(ctx, repoID)
	}()
}

func (c *Controller) handlePublish(ctx context.Context, repoID string) {
	if c.cfg.Shouts == nil || c.cfg.Prompter == nil {
		return
	}
	vm := c.cfg.ViewModel()
	if !vm.Connected || vm.Stale || !vm.CanPublish() {
		return
	}
	result, err := c.cfg.Prompter.PromptText(ctx, platform.PromptTextRequest{
		Title:       "Opublikuj wydanie",
		Text:        "Komentarz wydania (widoczny dla zespołu po aktualizacji):",
		Placeholder: "np. materiały na jutrzejsze spotkanie",
	})
	if err != nil || result.Cancelled {
		return
	}
	rev, err := c.cfg.Shouts.Publish(ctx, repoID, result.Value)
	if err != nil {
		title, body, infoOnly := publishPresentation(err)
		if infoOnly {
			c.notify(ctx, platform.Notification{ID: "shout", Group: "shout", Title: title, Body: body, Urgency: platform.UrgencyNormal})
			_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: title, Text: body})
			return
		}
		c.reportActionError(ctx, "shout", title, body)
		return
	}
	title := "Wydanie opublikowane"
	body := fmt.Sprintf("Zmiany zapisano jako rewizję r%d. Zespół zobaczy komentarz po aktualizacji.", rev)
	c.notify(ctx, platform.Notification{ID: "shout", Group: "shout", Title: title, Body: body, Urgency: platform.UrgencyNormal})
	_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: title, Text: body})
	if c.cfg.Refresh != nil {
		c.cfg.Refresh()
	}
}

func (c *Controller) startAckNotice(ctx context.Context, noticeID string) {
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		if c.cfg.Notices == nil || noticeID == "" {
			return
		}
		if err := c.cfg.Notices.AckNotice(ctx, noticeID); err != nil {
			c.reportActionError(ctx, "shout", "Nie udało się potwierdzić wydania", err.Error())
			return
		}
		if c.cfg.Refresh != nil {
			c.cfg.Refresh()
		}
	}()
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

func (c *Controller) startLockUnlock(ctx context.Context, repoID string, lock bool, actionID string) {
	key := "mutate:" + repoID
	if repoID == "" || !c.beginOperation(key) {
		c.finishProjectedAction(actionID)
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		if c.handleLockUnlock(ctx, repoID, lock) {
			c.awaitProjectedAction(actionID)
		} else {
			c.finishProjectedAction(actionID)
		}
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

func (c *Controller) startReservationRelease(ctx context.Context, reservationID, actionID string) {
	if strings.TrimSpace(reservationID) == "" || c.cfg.Reservations == nil || c.cfg.Prompter == nil || !c.beginOperation("release-reservation:"+reservationID) {
		c.finishProjectedAction(actionID)
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("release-reservation:" + reservationID)
		if c.handleReservationRelease(ctx, reservationID) {
			c.awaitProjectedAction(actionID)
		} else {
			c.finishProjectedAction(actionID)
		}
	}()
}

func (c *Controller) handleReservationRelease(ctx context.Context, reservationID string) bool {
	vm := c.cfg.ViewModel()
	reservation, ok := findReservation(vm, reservationID)
	if !ok || !reservation.CanRelease || !vm.CanReleaseReservations() || !viewHasServer(vm, reservation.ServerID) {
		return false
	}

	server, ok := findServer(vm, reservation.ServerID)
	if !ok {
		return false
	}
	risk := reservation.LocalChanges || reservation.ActivePassport
	text := fmt.Sprintf("%s\nKopia robocza: %s\n\nZwolnienie odbierze blokadę SVN innym osobom.", reservationDisplayPath(reservation.WorkingCopy, reservation.Path), reservationWorkingCopyAlias(server, reservation))
	if risk {
		text += "\n\nTen folder ma lokalne zmiany lub aktywny paszport edycji. Otwarte programy mogą mieć niezapisane dane; FileES nie bada uchwytów otwartych przez edytory. Kontynuować świadomie?"
	}
	confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: "Zwolnij rezerwację", Text: text, ConfirmText: "Zwolnij", CancelText: "Anuluj"})
	if err != nil || !confirmed || ctx.Err() != nil {
		return false
	}

	// Resolve the opaque row again after the prompt. The ID contains the lock
	// generation, while ExpectedToken remains entirely inside Go and fences
	// the daemon operation against a newer lock on the same path.
	vm = c.cfg.ViewModel()
	reservation, ok = findReservation(vm, reservationID)
	if !ok || !reservation.CanRelease || !vm.CanReleaseReservations() || !viewHasServer(vm, reservation.ServerID) {
		return false
	}
	risk = reservation.LocalChanges || reservation.ActivePassport
	err = c.cfg.Reservations.ReleaseReservation(ctx, app.ReservationReleaseRequest{
		ServerID: reservation.ServerID, RepoID: reservation.RepoID, Path: reservation.Path,
		ExpectedToken: reservation.Token, ConfirmRisk: risk,
	})
	if err != nil {
		if ctx.Err() == nil {
			c.notify(ctx, platform.Notification{ID: "release_reservation." + reservation.ServerID, Group: "release_reservation." + reservation.ServerID, Title: "Nie można zwolnić rezerwacji", Body: err.Error(), Urgency: platform.UrgencyNormal})
		}
		return false
	}
	c.notify(ctx, platform.Notification{ID: "release_reservation." + reservation.ServerID, Group: "release_reservation." + reservation.ServerID, Title: "Zwolniono rezerwację", Body: reservationDisplayPath(reservation.WorkingCopy, reservation.Path), Urgency: platform.UrgencyLow})
	return true
}

func findReservation(vm app.ViewModel, reservationID string) (app.Reservation, bool) {
	for _, reservation := range vm.Reservations {
		if reservation.ID == reservationID {
			return reservation, true
		}
	}
	return app.Reservation{}, false
}

func findServer(vm app.ViewModel, serverID string) (app.ServerViewModel, bool) {
	for _, server := range vm.Servers {
		if server.ID == serverID {
			return server, true
		}
	}
	return app.ServerViewModel{}, false
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

func (c *Controller) handleLockUnlock(ctx context.Context, repoID string, lock bool) bool {
	vm := c.cfg.ViewModel()
	if !canMutate(vm, lock) {
		return false
	}
	repo, ok := findRepo(vm, repoID)
	if !ok || repo.LocalPath == "" || !repo.CanWrite() {
		return false
	}
	if !lock && repo.ReservationCount == 0 {
		return false
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
			return false
		}
		c.notify(ctx, platform.Notification{
			ID:      opName + "." + repoID,
			Group:   opName + "." + repoID,
			Title:   "Błąd wyboru plików",
			Body:    err.Error(),
			Urgency: platform.UrgencyNormal,
		})
		return false
	}
	if result.Cancelled || len(result.Paths) == 0 {
		return false
	}

	// Re-check the full mutable state: the daemon or repository configuration
	// may have changed while the native picker was open.
	vm = c.cfg.ViewModel()
	if !canMutate(vm, lock) {
		return false
	}
	currentRepo, ok := findRepo(vm, repoID)
	if !ok || !currentRepo.CanWrite() || currentRepo.LocalPath == "" || filepath.Clean(currentRepo.LocalPath) != filepath.Clean(repo.LocalPath) {
		return false
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
		return false
	}

	var opErr error
	if lock {
		_, opErr = c.cfg.Locker.Lock(ctx, repoID, paths)
	} else {
		_, opErr = c.cfg.Locker.Unlock(ctx, repoID, paths)
	}
	if ctx.Err() != nil {
		return false
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
		return false
	}
	c.notify(ctx, platform.Notification{
		ID:      opName + "." + repoID,
		Group:   opName + "." + repoID,
		Title:   fmt.Sprintf("%s %d plik(ów)", successNoun, len(paths)),
		Body:    lockNotificationPaths(repo.LocalPath, paths),
		Urgency: platform.UrgencyLow,
	})
	return true
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

func (c *Controller) awaitProjectedAction(actionID string) {
	if actionID != "" && c.cfg.ActionLifecycle != nil {
		c.cfg.ActionLifecycle.AwaitActionProjection(actionID)
		return
	}
	if c.cfg.Refresh != nil {
		c.cfg.Refresh()
	}
}

func (c *Controller) startProjectedAction(action app.PendingAction) string {
	if c.cfg.ActionLifecycle == nil {
		return ""
	}
	action.ID = action.Kind + ":" + strconv.FormatUint(c.actionSeq.Add(1), 10)
	action.StartedAt = time.Now()
	if !c.cfg.ActionLifecycle.StartAction(action) {
		return ""
	}
	return action.ID
}

func (c *Controller) finishProjectedAction(actionID string) {
	if actionID != "" && c.cfg.ActionLifecycle != nil {
		c.cfg.ActionLifecycle.FinishAction(actionID)
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
	if detailed := detailedMessageLabel(message, structured.PresentationDetails()); detailed != "" {
		body = detailed
	}
	if label := hintLabel(hint); label != "" {
		body += " — " + label
	}
	if severity == "FATAL" || severity == "ERROR" {
		urgency = platform.UrgencyCritical
	}
	return title, body, urgency
}

// detailedMessageLabel builds the sentence a user can act on for keys that
// define structured fields. It returns "" when the key has none, or when the
// fields are missing, so the caller keeps the plain label rather than
// rendering a sentence with holes in it.
//
// This exists because "the file is busy" is not actionable while "Anna has it
// until 13:41" is: the whole point of naming the holder is that the reader
// knows who to go and ask.
func detailedMessageLabel(messageKey string, details map[string]string) string {
	return errcat.PolishDetailed(messageKey, details)
}

func messageLabel(messageKey string) string {
	return errcat.Polish(messageKey)
}

func actionErrorBody(err error) string {
	if err == nil {
		return ""
	}
	var structured presentationError
	if !errors.As(err, &structured) {
		return err.Error()
	}
	_, _, _, key := structured.PresentationError()
	details := structured.PresentationDetails()
	if key == "repo.locate_failed" {
		if reason := strings.TrimSpace(details["detail"]); reason != "" {
			return locateFailurePolish(reason)
		}
	}
	if detailed := detailedMessageLabel(key, details); detailed != "" {
		return detailed
	}
	return messageLabel(key)
}

func publishPresentation(err error) (title, body string, infoOnly bool) {
	title = "Nie udało się opublikować wydania"
	body = actionErrorBody(err)
	var structured presentationError
	if !errors.As(err, &structured) {
		return title, body, false
	}
	_, _, _, key := structured.PresentationError()
	switch key {
	case "shout.nothing_to_publish":
		return "Brak zmian do opublikowania", body, true
	case "shout.invalid_comment":
		return "Nieprawidłowy komentarz wydania", body, false
	case "shout.read_only":
		return "Repozytorium jest tylko do odczytu", body, false
	default:
		return title, body, false
	}
}

func locateFailurePolish(raw string) string {
	switch {
	case strings.TrimSpace(raw) == "":
		return "Wskazany folder nie jest kopią roboczą tego udziału."
	case strings.Contains(raw, "not a Subversion working copy"):
		return "Wskazany folder nie jest kopią roboczą Subversion."
	case strings.Contains(raw, "does not match projected"):
		return "Wskazany folder należy do innego repozytorium."
	case strings.Contains(raw, "working-copy identity"):
		return "Wskazany folder nie ma tożsamości tego udziału FileES."
	case strings.Contains(raw, "overlaps"), strings.Contains(raw, "disjoint"):
		return "Wskazany folder nachodzi na już zapisaną kopię FileES."
	default:
		if errcat.KnownKey(raw) {
			return errcat.Polish(raw)
		}
		return "Wskazany folder nie jest kopią roboczą tego udziału."
	}
}

func hintLabel(hint string) string {
	return errcat.PolishHint(hint)
}

func (c *Controller) notify(ctx context.Context, n platform.Notification) {
	if c.cfg.Notifier == nil {
		return
	}
	_ = c.cfg.Notifier.Notify(ctx, n)
}

// reportActionError keeps an explicitly initiated foreground workflow from
// degrading into a toast-only failure. The notification remains useful for
// the system history, while ShowInfo guarantees an owned modal explanation
// when the platform provides one.
func (c *Controller) reportActionError(ctx context.Context, group, title, body string) {
	c.notify(ctx, platform.Notification{ID: group, Group: group, Title: title, Body: body, Urgency: platform.UrgencyCritical})
	if c.cfg.Prompter != nil {
		_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{Title: title, Text: body})
	}
}

func findRepo(vm app.ViewModel, repoID string) (app.RepoViewModel, bool) {
	for _, r := range vm.Repos {
		if r.ID == repoID {
			return r, true
		}
	}
	return app.RepoViewModel{}, false
}

// startSetEditingPolicy flips one owned repository between plain
// merge-on-commit and edit passports. The confirmation is deliberately blunt
// about consequences on both sides: turning it on marks every file in the
// repository as needing a lock and commits that, and turning it off removes
// those marks again. Neither is a preference toggle - both rewrite versioned
// properties that every other client will see.
func (c *Controller) startSetEditingPolicy(ctx context.Context, serverID, repoID string) {
	key := "editing-policy." + serverID + "." + repoID
	if serverID == "" || repoID == "" || c.cfg.RealmGrants == nil || c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		vm := c.cfg.ViewModel()
		if !vm.CanSetEditingPolicy() {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Zasady edycji są niedostępne", Body: "Demon FileES nie obsługuje zmiany zasad edycji repozytorium.", Urgency: platform.UrgencyCritical})
			return
		}
		var repo app.RepoViewModel
		found := false
		for _, candidate := range vm.Repos {
			if candidate.ID == repoID && candidate.ServerID == serverID {
				repo, found = candidate, true
				break
			}
		}
		if !found {
			return
		}
		name := repo.DisplayName
		if strings.TrimSpace(name) == "" {
			name = repoID
		}

		title := "Włączyć wypożyczanie plików?"
		text := "Repozytorium „" + name + "” przejdzie na pracę z wypożyczeniami.\n\n" +
			"Każdy plik zostanie oznaczony jako wymagający wypożyczenia i ta zmiana zostanie opublikowana — zobaczą ją wszystkie komputery podłączone do tego repozytorium. " +
			"Od tej pory pliki będą tylko do odczytu, dopóki ktoś ich nie wypożyczy, a dwie osoby nie zmienią naraz tego samego pliku.\n\n" +
			"Jeżeli masz teraz niezapisane zmiany, zmiana poczeka, aż zostaną opublikowane."
		lockRequired := true
		if repo.RequiresLock() {
			title = "Wyłączyć wypożyczanie plików?"
			text = "Repozytorium „" + name + "” wróci do pracy bez wypożyczeń.\n\n" +
				"Oznaczenia wymagające wypożyczenia zostaną zdjęte z plików i ta zmiana zostanie opublikowana. " +
				"Pliki znów będą edytowalne od razu, ale dwie osoby będą mogły zmienić ten sam plik równocześnie."
			lockRequired = false
		}
		confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{Title: title, Text: text, ConfirmText: "Zastosuj", CancelText: "Anuluj"})
		if err != nil || !confirmed {
			return
		}
		stored, err := c.cfg.RealmGrants.SetEditingPolicy(ctx, serverID, repoID, lockRequired)
		if err != nil {
			c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Nie udało się zmienić zasad edycji", Body: err.Error(), Urgency: platform.UrgencyCritical})
			return
		}
		body := "Pliki są znów edytowalne bez wypożyczania."
		if stored {
			body = "Pliki wymagają teraz wypożyczenia przed edycją. Zmiana dotrze do pozostałych komputerów przy najbliższym odświeżeniu."
		}
		c.notify(ctx, platform.Notification{ID: key, Group: key, Title: "Zasady edycji zostały zmienione", Body: body, Urgency: platform.UrgencyNormal})
	}()
}
