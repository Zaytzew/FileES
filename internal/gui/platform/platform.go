// Package platform defines the operating-system boundary used by the GUI.
// It contains no platform implementation and no daemon/IPC concepts.
package platform

import (
	"context"
	"strings"
)

// Backend contains operating-system services, not dialog renderers.
// Wails supplies file pickers and all application dialogs separately.
type Backend interface {
	FolderOpener
	Notifier
	Autostart
}

type Prompter interface {
	PromptText(ctx context.Context, request PromptTextRequest) (PromptTextResult, error)
	ShowInfo(ctx context.Context, request InfoRequest) error
	Confirm(ctx context.Context, request ConfirmRequest) (bool, error)
}

type ConsentPrompter interface {
	ConfirmConsent(context.Context, ConsentRequest) (ConsentResult, error)
}

type ConsentRequest struct {
	// Marks GUI-authored required/optional templates; empty preserves raw copy.
	PresentationKey                         string
	Title, Text, RequiredText, OptionalText string
}

type ConsentResult struct {
	Cancelled, Required, Optional bool
}

// ReservationBrowser renders the native, server-scoped reservation window.
// It receives display-only data and returns an opaque row ID; token fencing and
// all SVN work remain in the GUI action/daemon layers.
type ReservationBrowser interface {
	ShowReservations(ctx context.Context, request ReservationDialogRequest) (ReservationDialogResult, error)
}

// SettingsBrowser renders a server-scoped management overview (or the narrow
// recovery view). Actions are kept out of this boundary until the controller
// has validated the user's intent.
type SettingsBrowser interface {
	ShowSettings(ctx context.Context, request SettingsDialogRequest) (SettingsDialogResult, error)
}

// ProgressPresenter renders a modeless "work in progress" window for an
// operation whose duration the user cannot otherwise see.
//
// It is deliberately not part of Backend: every other surface here is a modal
// that ends when the *user* acts, while this one ends when the *controller*
// says so. Keeping it optional lets lightweight backends and tests ignore it,
// exactly like ConsentPrompter.
//
// ShowProgress returns a close function that is safe to call once and blocks
// until the window is gone. Implementations must not report progress values:
// the daemon does not measure import progress, and a fake percentage is worse
// than an honest "still working".
type ProgressPresenter interface {
	ShowProgress(ctx context.Context, request ProgressRequest) (close func(), err error)
}

type ProgressRequest struct {
	Title, Text string
}

// JournalBrowser renders the combined activity and error history. Rows are
// already aggregated and ordered by the presentation layer; the platform only
// owns native rendering (including emphasis for errors).
type JournalBrowser interface {
	ShowJournal(ctx context.Context, request JournalDialogRequest) error
}

type JournalDialogRequest struct {
	Title string
	Text  string
	Rows  []JournalDialogRow
}

type JournalDialogRow struct {
	Timestamp  string
	Repository string
	Summary    string
	Details    string
	Severity   string
	Emphasized bool
}

type RealmGrantBrowser interface {
	ShowRealmGrants(context.Context, RealmGrantDialogRequest) (RealmGrantDialogResult, error)
	ShowRealmVisibility(context.Context, RealmVisibilityDialogRequest) (RealmVisibilityDialogResult, error)
}

// PublicShareBrowser renders the owner's channels for one repository. It
// returns only an action and opaque channel ID; declarations and secrets are
// collected by the controller after the list window has closed.
type PublicShareBrowser interface {
	ShowPublicShares(context.Context, PublicShareDialogRequest) (PublicShareDialogResult, error)
}

type PublicShareDialogRequest struct {
	TextKey        string // GUI-authored description; empty preserves Text verbatim.
	Title          string
	Text           string
	ServerID       string
	RepoID         string
	RepositoryName string
	FocusChannelID string
	DirectEntry    bool
	Shares         []PublicShareSummary
}

type PublicShareSummary struct {
	StateKey                                                              string
	ChannelID, Address, State, SourceRoot, Recipients, Password, Revision string
}

type PublicShareDialogAction string

const (
	PublicShareDialogClose  PublicShareDialogAction = "close"
	PublicShareDialogCreate PublicShareDialogAction = "create"
	PublicShareDialogEdit   PublicShareDialogAction = "edit"
	PublicShareDialogRevoke PublicShareDialogAction = "revoke"
	PublicShareDialogDelete PublicShareDialogAction = "delete"
)

type PublicShareDialogResult struct {
	Action    PublicShareDialogAction
	ChannelID string
}

// UploadChannelBrowser renders the owner's intake shelves for one repository.
// It returns only an action and opaque channel ID; slug and recipients are
// collected by the controller after the list window has closed.
type UploadChannelBrowser interface {
	ShowUploadChannels(context.Context, UploadChannelDialogRequest) (UploadChannelDialogResult, error)
}

type UploadChannelDialogRequest struct {
	// DirectEntry carries an already-authorized parent context from a shelf action.
	DirectEntry                      bool
	ServerID, RepoID, RepositoryName string
	TextKey                          string // GUI-authored description; empty preserves Text verbatim.
	Title                            string
	Text                             string
	Channels                         []UploadChannelSummary
}

type UploadChannelSummary struct {
	StateKey                              string
	ChannelID, Address, State, Recipients string
	RequireOTP                            bool
}

type UploadChannelDialogAction string

const (
	UploadChannelDialogClose  UploadChannelDialogAction = "close"
	UploadChannelDialogCreate UploadChannelDialogAction = "create"
	UploadChannelDialogEdit   UploadChannelDialogAction = "edit"
	UploadChannelDialogRevoke UploadChannelDialogAction = "revoke"
	UploadChannelDialogDelete UploadChannelDialogAction = "delete"
	// Browse opens one shelf's contents. It is the only entry: a shelf is
	// reached through the channel that owns it, never guessed from a
	// repository row, because the channel is what names it.
	UploadChannelDialogBrowse UploadChannelDialogAction = "browse"
)

type UploadChannelDialogResult struct {
	Action    UploadChannelDialogAction
	ChannelID string
}

// ShelfBrowser renders one upload shelf: what contributors have delivered and
// the owner has not dealt with yet.
//
// Modelled on QuarantineBrowser, and the differences are the shelf's own. The
// waiting room holds rejected payloads on the server's disk and hands them over
// as bytes; a shelf holds accepted files inside the delivery repository, so
// nothing here carries content. A row names what is there and where it sits in
// that repository, and the file itself travels by svn checkout when the owner
// asks for it — the single direction the concept allows (§10a.0).
//
// There is no hide. A reject expires by itself, so hiding one is housekeeping;
// a shelf entry stands until the owner takes it, which is the point of a shelf.
type ShelfBrowser interface {
	ShowShelf(context.Context, ShelfDialogRequest) (ShelfDialogResult, error)
}

type ShelfDialogRequest struct {
	CanImport                     bool
	TextKey                       string // GUI-authored description; empty preserves Text verbatim.
	TextPrefix                    string // Literal server message preceding GUI copy.
	Title, Text, ServerID, RepoID string
	RepositoryName                string
	// ChannelID names the shelf. An owner may hold several on one repository,
	// so the dialog is never opened for "the" shelf.
	ChannelID   string
	ShelfName   string
	Items       []ShelfItem
	DirectEntry bool
}

// ShelfItem is one delivered file. RepoPath is what a fetch will ask the
// delivery repository for, so it travels rather than being rebuilt from the
// original name, which the naming policy may have changed.
//
// No contributor is named. Which invitation was exercised lives in the
// encrypted event record on the server and is resolvable only with a key kept
// apart from it; a browser row is not where that is handed out.
type ShelfItem struct {
	UploadID, RepoPath, OriginalName string
	Size                             int64
	Revision                         int64
	AcceptedAt                       string
}

type ShelfDialogAction string

const (
	ShelfDialogClose  ShelfDialogAction = "close"
	ShelfDialogFetch  ShelfDialogAction = "fetch"
	ShelfDialogImport ShelfDialogAction = "import"
)

type ShelfDialogResult struct {
	Action   ShelfDialogAction
	UploadID string
}

// QuarantineBrowser renders the owner's AV-reject waiting room as a daemon
// projection. Fetch copies bytes locally; hide only drops the row from the
// manifest. Closing is silent: no download, no hide.
type QuarantineBrowser interface {
	ShowQuarantine(context.Context, QuarantineDialogRequest) (QuarantineDialogResult, error)
}

type QuarantineDialogRequest struct {
	TextKey                       string // GUI-authored description; empty preserves Text verbatim.
	TextPrefix                    string // Literal server message preceding GUI copy.
	Title, Text, ServerID, RepoID string
	RepositoryName                string
	Items                         []QuarantineItem
	DirectEntry                   bool
}

type QuarantineItem struct {
	UploadID, OriginalName, AVVerdict string
	Size                              int64
	RemainingHours                    int
}

type QuarantineDialogAction string

const (
	QuarantineDialogClose QuarantineDialogAction = "close"
	QuarantineDialogFetch QuarantineDialogAction = "fetch"
	QuarantineDialogHide  QuarantineDialogAction = "hide"
)

type QuarantineDialogResult struct {
	Action   QuarantineDialogAction
	UploadID string
}

type RealmGrantDialogRequest struct {
	TextKey    string // GUI-authored description; empty preserves Text verbatim.
	Title      string
	Text       string
	Recipients []RealmGrantRecipient
}

type RealmGrantRecipient struct {
	RealmID string
	Alias   string
	Access  string
	State   string
}

type RealmGrantDialogAction string

const (
	RealmGrantDialogClose  RealmGrantDialogAction = "close"
	RealmGrantDialogRead   RealmGrantDialogAction = "grant_read"
	RealmGrantDialogWrite  RealmGrantDialogAction = "grant_write"
	RealmGrantDialogRevoke RealmGrantDialogAction = "revoke"
)

type RealmGrantDialogResult struct {
	Action  RealmGrantDialogAction
	RealmID string
}

type RealmVisibilityDialogRequest struct {
	RealmName string
	Title     string
	Text      string
}

type RealmVisibilityDialogAction string

const (
	RealmVisibilityDialogClose   RealmVisibilityDialogAction = "close"
	RealmVisibilityDialogListed  RealmVisibilityDialogAction = "listed"
	RealmVisibilityDialogPrivate RealmVisibilityDialogAction = "hidden"
)

type RealmVisibilityDialogResult struct {
	Action RealmVisibilityDialogAction
}

type SettingsDialogRequest struct {
	TextKey string // GUI-authored description; empty preserves Text verbatim.
	Title   string
	Text    string
	// FocusRepoID asks contextual renderers to present actions for one
	// already-validated folder. Other renderers may use the filtered list.
	FocusRepoID string
	Servers     []SettingsServer
	Recoveries  []SettingsRecovery
}

type SettingsServer struct {
	ID, Name, Address, Realm, ClientID string
	CanSetRealmVisibility              bool
	CanSetRealmBranding                bool
	CanClaimRealmAlias                 bool
	CanPairMobile                      bool
	// CanAddFolder mirrors startCreateRepository's own guard
	// (server.CanOfferRepositoryCreation(), e.g. false for a read-only
	// client role such as an audit-only client) -- add_folder used to be
	// offered unconditionally, so a restricted client saw a real "nothing
	// happens" click with zero feedback.
	CanAddFolder bool
	// CanSetSessionTimeout is a local setting: how long to wait for one
	// send or fetch. Offered on a real server row. Recovery rows leave it false.
	CanSetSessionTimeout bool
	SessionTimeoutMin    int
	Folders              []SettingsFolder
}

// SettingsFolder's Can* fields mirror the exact preconditions their
// corresponding controller action (startDetachRepository, startLoadDump in
// actions.go) checks before doing anything. Each was added after a live,
// reproducible "click it, nothing happens" bug: the dialog used to offer
// every action unconditionally once a folder was selected, while the
// controller's own guard silently returned with zero feedback (no dialog,
// no notification) when its precondition wasn't met. Keep these in sync
// with repositoryOwnedByCurrentRealm/CanDetachRepository/CanDeleteRepository
// rather than reintroducing an unconditional button.
type SettingsFolder struct {
	StateKey, AccessKey, EditingKey    string
	LastCommitAt                       string
	CanFoldInactive                    bool
	ID, Name, LocalPath, State, Access string
	// Editing is a human-readable rendering of the repository editing policy,
	// shown to every client rather than only the owner: a read-only file with
	// no stated reason is the confusing state this is meant to replace.
	Editing                 string
	CanManageGrants         bool
	CanSetEditingPolicy     bool // owner-only: switch between free and lock_required
	LockRequired            bool // current policy, for the action's confirmation text
	CanManagePublicShares   bool
	CanManageUploadChannels bool
	CanReviewQuarantine     bool
	CanConnect              bool // connect selected unattached repository
	CanLocate               bool // adopt an existing moved working copy
	CanDetach               bool // detach_folder (non-destructive)
	CanDelete               bool // delete_repository
	CanLoadDump             bool // load_dump
	CanRetryLifecycle       bool // retry the same durable local operation
	CanResolveIntents       bool // inspect daemon-owned uncertainty plan
	CanAbandonLifecycle     bool // end only the failed local attempt
}
type SettingsRecovery struct {
	OperationID, ServerName, KitPath, Status string
	CanDownload                              bool
}

type SettingsDialogAction string

const (
	SettingsDialogClose            SettingsDialogAction = "close"
	SettingsDialogAddFolder        SettingsDialogAction = "add_folder"
	SettingsDialogConnectRepos     SettingsDialogAction = "connect_repositories"
	SettingsDialogLocateFolder     SettingsDialogAction = "locate_folder"
	SettingsDialogDetachFolder     SettingsDialogAction = "detach_folder"
	SettingsDialogDeleteRepo       SettingsDialogAction = "delete_repository"
	SettingsDialogLoadDump         SettingsDialogAction = "load_dump"
	SettingsDialogRetryLifecycle   SettingsDialogAction = "retry_lifecycle"
	SettingsDialogResolveIntents   SettingsDialogAction = "resolve_intents"
	SettingsDialogAbandonLifecycle SettingsDialogAction = "abandon_lifecycle"
	SettingsDialogManageGrants     SettingsDialogAction = "manage_grants"
	SettingsDialogEditingPolicy    SettingsDialogAction = "editing_policy"
	SettingsDialogPublicShares     SettingsDialogAction = "public_shares"
	SettingsDialogUploadChannels   SettingsDialogAction = "upload_channels"
	SettingsDialogQuarantine       SettingsDialogAction = "quarantine"
	SettingsDialogRealmVisibility  SettingsDialogAction = "realm_visibility"
	SettingsDialogRealmBranding    SettingsDialogAction = "realm_branding"
	SettingsDialogRealmAlias       SettingsDialogAction = "realm_alias"
	SettingsDialogPairMobile       SettingsDialogAction = "pair_mobile"
	SettingsDialogSessionTimeout   SettingsDialogAction = "session_timeout"
	SettingsDialogDetachServer     SettingsDialogAction = "detach_server"
	SettingsDialogRemoveRealm      SettingsDialogAction = "remove_realm"
	SettingsDialogDownloadRecovery SettingsDialogAction = "download_recovery"
)

type SettingsDialogResult struct {
	Action           SettingsDialogAction
	ServerID, RepoID string
	RepoIDs          []string
	OperationID      string
}

// SettingsText is the accessible, compact fallback used by platforms that do
// not yet have a tabular settings surface.
func SettingsText(request SettingsDialogRequest) string {
	lines := make([]string, 0, 4+len(request.Servers)*4)
	if strings.TrimSpace(request.Text) != "" {
		lines = append(lines, request.Text)
	}
	for _, server := range request.Servers {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Serwer: "+server.Name, "Adres: "+server.Address, "Strefa: "+server.Realm, "ID klienta: "+server.ClientID)
		if len(server.Folders) == 0 {
			lines = append(lines, "Repozytoria: brak")
			continue
		}
		lines = append(lines, "Repozytoria:")
		for _, folder := range server.Folders {
			lines = append(lines, "• "+folder.Name, "  "+folder.LocalPath+" — "+folder.State+", "+folder.Access)
		}
	}
	for _, recovery := range request.Recoveries {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Odzyskiwanie: "+recovery.ServerName, recovery.Status, "Pakiet: "+recovery.KitPath)
	}
	if len(lines) == 0 {
		return "Brak aktywnych serwerów FileES."
	}
	return strings.Join(lines, "\n")
}

type ReservationDialogRequest struct {
	Title string
	Text  string
	Rows  []ReservationDialogRow
}

type ReservationDialogRow struct {
	ID          string
	Server      string
	WorkingCopy string
	Path        string
	Owner       string
	CreatedAt   string
	Action      string
}

type ReservationDialogAction string

const (
	ReservationDialogClose      ReservationDialogAction = "close"
	ReservationDialogRefresh    ReservationDialogAction = "refresh"
	ReservationDialogRelease    ReservationDialogAction = "release"
	ReservationDialogReleaseAll ReservationDialogAction = "release_all"
)

type ReservationDialogResult struct {
	Action ReservationDialogAction
	RowID  string
}

type InfoRequest struct {
	// PresentationKey identifies GUI copy; arguments remain literal data.
	// Empty means preserve the supplied title and body unchanged.
	PresentationKey  string
	PresentationArgs map[string]string
	Title            string
	Text             string
}

type ConfirmRequest struct {
	// PresentationKey identifies a GUI-authored template, never a daemon
	// message or action. Empty means display the supplied text unchanged.
	PresentationKey string
	// PresentationArgs are literal display data, never executable templates.
	PresentationArgs map[string]string
	Title            string
	Text             string
	ConfirmText      string
	CancelText       string
}

type PromptTextRequest struct {
	// PresentationKey marks a GUI-owned template; defaults remain literal input.
	PresentationKey  string
	PresentationArgs map[string]string
	Title            string
	Text             string
	// Label names the value above native/browser form controls. Empty keeps the
	// generic presenter default for callers that do not need a domain label.
	Label string
	// Placeholder is a hint shown in an empty field and never submitted.
	// Default is a real starting value the user edits and may submit as-is.
	//
	// These were one field, which quietly did the wrong thing for half its
	// callers: the hint was written into the field as content, so "Kod OTP" or
	// "filees-invite:v1:…" arrived as the answer if the user just pressed OK -
	// and with Secret set it was masked, so they could not even see what they
	// were about to send.
	Placeholder string
	Default     string
	Secret      bool
}

type PromptTextResult struct {
	Value     string
	Cancelled bool
}

type FolderOpener interface {
	OpenFolder(ctx context.Context, path string) error
}

type FilePicker interface {
	PickFiles(ctx context.Context, request PickFilesRequest) (PickFilesResult, error)
}

type FolderPicker interface {
	PickFolder(ctx context.Context, request PickFolderRequest) (PickFolderResult, error)
}

type PickFolderRequest struct {
	Title      string
	InitialDir string
}

type PickFolderResult struct {
	Path      string
	Cancelled bool
}

type Notifier interface {
	Notify(ctx context.Context, notification Notification) error
}

type Autostart interface {
	AutostartStatus(ctx context.Context, spec AutostartSpec) (AutostartState, error)
	SetAutostart(ctx context.Context, spec AutostartSpec, enabled bool) error
}

// PickFilesRequest describes a native file picker without prescribing its UI
// toolkit. Root is normally the repository boundary presented to the user;
// local presentation assets may explicitly opt out without weakening any
// repository operation.
type PickFilesRequest struct {
	Title         string
	Root          string
	InitialDir    string
	AllowMultiple bool
	// AllowOutsideRoot is reserved for local presentation assets such as a
	// realm logo. Repository operations must leave it false: their selected
	// paths are deliberately confined to Root.
	AllowOutsideRoot bool
}

// PickFilesResult treats user cancellation as a normal outcome, not an error.
type PickFilesResult struct {
	Paths     []string
	Cancelled bool
}

// Notification is intentionally informational. It contains no callback or
// mutating action, so a platform adapter cannot bypass the GUI action boundary.
type Notification struct {
	ID      string
	Group   string
	Title   string
	Body    string
	Urgency Urgency
}

type Urgency string

const (
	UrgencyLow      Urgency = "low"
	UrgencyNormal   Urgency = "normal"
	UrgencyCritical Urgency = "critical"
)

// AutostartSpec identifies the per-user application entry. Executable must be
// absolute before it reaches a native adapter.
type AutostartSpec struct {
	ID         string
	Name       string
	Executable string
	Args       []string
}

type AutostartState struct {
	Enabled bool
	Current bool   // enabled entry launches the executable and arguments from the supplied spec
	Source  string // adapter-specific diagnostic label, never interpreted by app
}
