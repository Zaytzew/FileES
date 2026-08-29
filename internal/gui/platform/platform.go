// Package platform defines the operating-system boundary used by the GUI.
// It contains no platform implementation and no daemon/IPC concepts.
package platform

import (
	"context"
	"strings"
)

// Backend is the complete set of operating-system services required by the
// tray application. Consumers should depend on the smaller embedded interfaces
// whenever they need only one capability.
type Backend interface {
	FolderOpener
	FolderPicker
	FilePicker
	Prompter
	ConsentPrompter
	ReservationBrowser
	SettingsBrowser
	JournalBrowser
	RealmGrantBrowser
	PublicShareBrowser
	UploadChannelBrowser
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
	Title          string
	Text           string
	ServerID       string
	RepoID         string
	RepositoryName string
	Shares         []PublicShareSummary
}

type PublicShareSummary struct {
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
	Title    string
	Text     string
	Channels []UploadChannelSummary
}

type UploadChannelSummary struct {
	ChannelID, Address, State, Recipients string
}

type UploadChannelDialogAction string

const (
	UploadChannelDialogClose  UploadChannelDialogAction = "close"
	UploadChannelDialogCreate UploadChannelDialogAction = "create"
	UploadChannelDialogEdit   UploadChannelDialogAction = "edit"
	UploadChannelDialogRevoke UploadChannelDialogAction = "revoke"
	UploadChannelDialogDelete UploadChannelDialogAction = "delete"
)

type UploadChannelDialogResult struct {
	Action    UploadChannelDialogAction
	ChannelID string
}

type RealmGrantDialogRequest struct {
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
	Title string
	Text  string
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
	Title string
	Text  string
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
	CanConnect              bool // connect selected unattached repository
	CanLocate               bool // adopt an existing moved working copy
	CanDetach               bool // detach_folder (non-destructive)
	CanDelete               bool // delete_repository
	CanLoadDump             bool // load_dump
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
	SettingsDialogManageGrants     SettingsDialogAction = "manage_grants"
	SettingsDialogEditingPolicy    SettingsDialogAction = "editing_policy"
	SettingsDialogPublicShares     SettingsDialogAction = "public_shares"
	SettingsDialogUploadChannels   SettingsDialogAction = "upload_channels"
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
	Title string
	Text  string
}

type ConfirmRequest struct {
	Title       string
	Text        string
	ConfirmText string
	CancelText  string
}

type PromptTextRequest struct {
	Title string
	Text  string
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
