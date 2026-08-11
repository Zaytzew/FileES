package contract

import "encoding/json"

// Command name constants — stable identifiers sent in Request.Command.
// Names follow the <namespace>.<verb> pattern (§7).
const (
	// System
	CmdSystemHello    = "system.hello"    // capability negotiation
	CmdSystemStatus   = "system.status"   // daemon uptime and aggregate state
	CmdSystemRestart  = "system.restart"  // graceful daemon restart
	CmdSystemShutdown = "system.shutdown" // graceful stop (privileged)
	CmdUpdateStatus   = "update.status"   // signed release availability
	CmdUpdatePlan     = "update.plan"     // verified dry-run change plan
	CmdUpdateApply    = "update.apply"    // apply and request GUI restart

	// Client activation (executed by daemon; GUI only supplies user intent).
	CmdActivationBegin      = "activation.begin"
	CmdActivationFinish     = "activation.finish"
	CmdRealmAliasClaim      = "realm.alias_claim"
	CmdRealmGrantRecipients = "realm.grant_recipients"
	CmdRealmSetVisibility   = "realm.set_visibility"
	CmdServerDetach         = "server.detach"
	CmdRealmRemoveBegin     = "realm.remove_begin"
	CmdRealmRemoveConfirm   = "realm.remove_confirm"
	CmdRecoveryDownload     = "recovery.download"

	// Mobile pairing (Phase 2c): daemon mints a MOBILE_PAIRING token through
	// its own already-authenticated control-plane channel; the tray hands
	// the result to a separate helper process that renders it as a QR code.
	CmdMobilePairingBegin = "mobile_pairing.begin"

	// Repos
	CmdRepoList               = "repo.list"                // list all configured repos
	CmdRepoStatus             = "repo.status"              // snapshot of one repo
	CmdRepoPause              = "repo.pause"               // suspend automatic operations
	CmdRepoResume             = "repo.resume"              // resume after pause
	CmdRepoSyncNow            = "repo.sync_now"            // request immediate poll/update
	CmdRepoPublish            = "repo.publish"             // request immediate commit of pending changes
	CmdRepoCreateRequest      = "repo.create_request"      // persist intent; server work is a later stage
	CmdRepoAttachIntent       = "repo.attach_intent"       // persist local path choice; no checkout yet
	CmdRepoAttachApprove      = "repo.attach_approve"      // approve the persisted intent and start checkout
	CmdRepoRelocate           = "repo.relocate"            // approve relocation of an attached working copy
	CmdRepoLocate             = "repo.locate"              // rebind an attachment to an existing moved working copy
	CmdRepoLoadDump           = "repo.load_dump"           // load a user-supplied dump into a fresh, single-carrier-commit repo
	CmdRepoGrantAccess        = "repo.grant_access"        // grant r/rw access to a visible foreign realm
	CmdRepoRevokeAccess       = "repo.revoke_access"       // revoke a realm grant without deleting local data
	CmdRepoSetEditingPolicy   = "repo.set_editing_policy"  // owner-only: switch a repository between free and lock_required editing
	CmdRepoPublicShareList    = "repo.public_share_list"   // list owned public distribution channels
	CmdRepoPublicShareCreate  = "repo.public_share_create" // create an owned public distribution channel
	CmdRepoPublicShareUpdate  = "repo.public_share_update" // update one owned active channel
	CmdRepoPublicShareRevoke  = "repo.public_share_revoke" // revoke access while retaining the channel record
	CmdRepoPublicShareDelete  = "repo.public_share_delete" // delete policy while retaining the address tombstone
	CmdRepoDetach             = "repo.detach"              // detach one local working copy, preserving user data
	CmdRepoDelete             = "repo.delete"              // delete an owned server repository, then detach locally
	CmdRepoLifecycleStatus    = "repo.lifecycle_status"    // poll outcome of a create/attach/relocate operation by ID
	CmdRepoActivity           = "repo.activity"            // global recent synchronization activity snapshot
	CmdRepoLock               = "repo.lock"                // acquire SVN lock on one or more paths
	CmdRepoUnlock             = "repo.unlock"              // release SVN lock on one or more paths
	CmdRepoReservationList    = "repo.reservation_list"    // list live locks in this client's working copies for one server
	CmdRepoReservationRelease = "repo.reservation_release" // safely release one listed lock

	// Conflicts and user decisions
	CmdConflictList   = "conflict.list"   // list pending conflicts / interactions
	CmdConflictGet    = "conflict.get"    // details of one conflict
	CmdConflictDecide = "conflict.decide" // submit user decision

	// Notices (outbound notifications)
	CmdNoticeList = "notice.list" // list active notices
	CmdNoticeAck  = "notice.ack"  // mark notice as acknowledged

	// Structured error log
	CmdErrorList = "error.list" // recent error records
	CmdErrorGet  = "error.get"  // single error by ID

	// Event streaming
	CmdEventsSubscribe = "events.subscribe" // switch connection to event push mode
)

// Capability constants — the daemon advertises which commands are active (§12).
// GUI shows only capabilities declared in HelloResult.Capabilities.
// Only list commands that are actually implemented.
const (
	CapEventsSubscribe        = "events.subscribe"
	CapRepoLock               = "repo.lock"
	CapRepoUnlock             = "repo.unlock"
	CapRepoReservationList    = "repo.reservation_list"
	CapRepoReservationRelease = "repo.reservation_release"
	CapErrorList              = "error.list"
	CapActivationBegin        = "activation.begin"
	CapActivationFinish       = "activation.finish"
	CapRealmAliasClaim        = "realm.alias_claim"
	CapRealmGrantRecipients   = "realm.grant_recipients"
	CapRealmSetVisibility     = "realm.set_visibility"
	CapServerDetach           = "server.detach"
	CapRealmRemoveBegin       = "realm.remove_begin"
	CapRealmRemoveConfirm     = "realm.remove_confirm"
	CapRecoveryDownload       = "recovery.download"
	CapMobilePairingBegin     = "mobile_pairing.begin"
	CapRepoCreateRequest      = "repo.create_request"
	CapRepoAttachIntent       = "repo.attach_intent"
	CapRepoAttachApprove      = "repo.attach_approve"
	CapRepoRelocate           = "repo.relocate"
	CapRepoLocate             = "repo.locate"
	CapRepoLoadDump           = "repo.load_dump"
	CapRepoGrantAccess        = "repo.grant_access"
	CapRepoRevokeAccess       = "repo.revoke_access"
	CapRepoSetEditingPolicy   = "repo.set_editing_policy"
	CapRepoPublicShareList    = "repo.public_share_list"
	CapRepoPublicShareCreate  = "repo.public_share_create"
	CapRepoPublicShareUpdate  = "repo.public_share_update"
	CapRepoPublicShareRevoke  = "repo.public_share_revoke"
	CapRepoPublicShareDelete  = "repo.public_share_delete"
	CapRepoDetach             = "repo.detach"
	CapRepoDelete             = "repo.delete"
	CapRepoLifecycleStatus    = "repo.lifecycle_status"
	CapRepoActivity           = "repo.activity"
	CapSystemRestart          = "system.restart"
	CapSystemShutdown         = "system.shutdown"

	// Update capabilities are advertised only after the daemon wires a signed
	// release checker and transactional platform installer.
	CapUpdateStatus = "update.status"
	CapUpdatePlan   = "update.plan"
	CapUpdateApply  = "update.apply"

	// Not yet implemented — defined for future use but NOT in AllCapabilities.
	CapRepoPause      = "repo.pause"
	CapRepoSyncNow    = "repo.sync_now"
	CapConflictDecide = "conflict.decide"
	CapRepoPublish    = "repo.publish"
)

// AllCapabilities is the set of capabilities the running daemon actually supports.
// Clients must not call commands not listed here.
var AllCapabilities = []string{
	CapEventsSubscribe,
	CapRepoLock,
	CapRepoUnlock,
	CapRepoReservationList,
	CapRepoReservationRelease,
	CapErrorList,
	CapActivationBegin,
	CapActivationFinish,
	CapRealmAliasClaim,
	CapServerDetach,
	CapRealmRemoveBegin,
	CapRealmRemoveConfirm,
	CapRecoveryDownload,
	CapMobilePairingBegin,
	CapRepoCreateRequest,
	CapRepoAttachIntent,
	CapRepoAttachApprove,
	CapRepoRelocate,
	CapRepoLocate,
	CapRepoLoadDump,
	CapRepoDetach,
	CapRepoDelete,
	CapRepoLifecycleStatus,
}

// --- result and payload types ---

// HelloResult is the result payload for CmdSystemHello.
type HelloResult struct {
	DaemonVersion    string   `json:"daemon_version"`
	ProtocolVersions []string `json:"protocol_versions"`
	Capabilities     []string `json:"capabilities"`
}

// SystemStatusResult is the result for CmdSystemStatus.
type SystemStatusResult struct {
	State       string             `json:"state"` // "running" | "stopping"
	UptimeSec   int64              `json:"uptime_sec"`
	Repos       int                `json:"repos"`
	Activations []ActivationStatus `json:"activations"`
	Recoveries  []RecoveryStatus   `json:"recoveries,omitempty"`
	Update      *UpdateStatus      `json:"update,omitempty"`
}

type RecoveryStatus struct {
	OperationID     string `json:"operation_id"`
	ServerID        string `json:"server_id"`
	ServerName      string `json:"server_name"`
	KitPath         string `json:"kit_path"`
	AdminContact    string `json:"admin_contact,omitempty"`
	ArchiveCount    int    `json:"archive_count"`
	DownloadUntil   string `json:"download_until"`
	AdminGraceUntil string `json:"admin_grace_until"`
}

type RecoveryDownloadPayload struct {
	OperationID string `json:"operation_id"`
	OutputRoot  string `json:"output_root"`
}
type RecoveryDownloadResult struct {
	OperationID string   `json:"operation_id"`
	Paths       []string `json:"paths"`
}

type UpdateStatus struct {
	State            string `json:"state"` // current | available | planning | applying | restart_required | failed
	CurrentVersion   string `json:"current_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	ReleaseID        string `json:"release_id,omitempty"`
	Summary          string `json:"summary,omitempty"`
	RestartRequired  bool   `json:"restart_required"`
}

type UpdateChange struct {
	Action string `json:"action"` // add | update | remove | unchanged
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

type UpdatePlanResult struct {
	CurrentVersion   string         `json:"current_version"`
	AvailableVersion string         `json:"available_version"`
	ReleaseID        string         `json:"release_id"`
	Changes          []UpdateChange `json:"changes"`
	RestartRequired  bool           `json:"restart_required"`
}

type UpdateApplyResult struct {
	InstalledVersion string `json:"installed_version"`
	RestartRequired  bool   `json:"restart_required"`
}

type ActivationStatus struct {
	ServerID              string `json:"server_id"`
	DisplayName           string `json:"display_name"`
	ClientRole            string `json:"client_role"`
	RealmID               string `json:"realm_id,omitempty"`
	RealmAlias            string `json:"realm_alias,omitempty"`
	Address               string `json:"address,omitempty"`
	ClientID              string `json:"client_id,omitempty"`
	SSHPort               int    `json:"ssh_port,omitempty"`
	CanCreateRepositories bool   `json:"can_create_repositories"`
	RepositoriesReady     bool   `json:"repositories_ready"`
	PendingRequiredRepos  int    `json:"pending_required_repositories"`
}

type ActivationBeginPayload struct {
	ServerID       string `json:"server_id"`
	ServerAddress  string `json:"server_address"`
	KnownHostsPath string `json:"known_hosts_path"`
	StateRoot      string `json:"state_root"`
	Invitation     string `json:"invitation,omitempty"`
	Email          string `json:"email"`
}

type ActivationFinishPayload struct {
	ServerID       string `json:"server_id"`
	ServerAddress  string `json:"server_address"`
	KnownHostsPath string `json:"known_hosts_path"`
	StateRoot      string `json:"state_root"`
	RemotePort     int    `json:"remote_port"`
	OTP            Secret `json:"otp"`
}

// RealmAliasClaimPayload asks the daemon to make the authenticated realm's
// immutable alias claim on the server. There is deliberately no companion
// "availability" request.
type RealmAliasClaimPayload struct {
	ServerID string `json:"server_id"`
	Alias    string `json:"alias"`
}

type RealmAliasClaimResult struct {
	Alias string `json:"alias"`
}

type RealmGrantRecipientsPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id,omitempty"`
}

type RealmGrantRecipient struct {
	RealmID string `json:"realm_id"`
	Alias   string `json:"alias"`
	Access  string `json:"access,omitempty"`
	State   string `json:"state,omitempty"`
}

type RealmGrantRecipientsResult struct {
	Recipients []RealmGrantRecipient `json:"recipients"`
}

type RealmSetVisibilityPayload struct {
	ServerID   string `json:"server_id"`
	Visibility string `json:"visibility"`
}

type RealmSetVisibilityResult struct {
	Visibility string `json:"visibility"`
}

type ServerDetachPayload struct {
	ServerID string `json:"server_id"`
}

type ServerDetachResult struct {
	ServerID string `json:"server_id"`
}

type RealmRemoveBeginPayload struct {
	ServerID          string `json:"server_id"`
	NotificationEmail string `json:"notification_email"`
	RecoveryDirectory string `json:"recovery_directory"`
	ErasureRequested  bool   `json:"erasure_requested"`
}
type RealmRemoveBeginResult struct {
	ServerID             string `json:"server_id"`
	OperationID          string `json:"operation_id"`
	RecoveryKitPath      string `json:"recovery_kit_path"`
	ExpiresAt            string `json:"expires_at"`
	ActiveClientCount    int    `json:"active_client_count"`
	OwnedRepositoryCount int    `json:"owned_repository_count"`
	ForeignGrantCount    int    `json:"foreign_grant_count"`
	AdminContact         string `json:"admin_contact"`
}
type RealmRemoveConfirmPayload struct {
	ServerID        string `json:"server_id"`
	OperationID     string `json:"operation_id"`
	RecoveryKitPath string `json:"recovery_kit_path"`
	OTP             Secret `json:"otp"`
}
type RealmRemoveConfirmResult struct {
	ServerID         string `json:"server_id"`
	OperationID      string `json:"operation_id"`
	RecoveryKitPath  string `json:"recovery_kit_path"`
	ArchiveCount     int    `json:"archive_count"`
	DownloadUntil    string `json:"download_until"`
	AdminGraceUntil  string `json:"admin_grace_until"`
	ErasureRequested bool   `json:"erasure_requested"`
	ErasureMaxDays   int    `json:"erasure_max_days,omitempty"`
}

// Secret preserves the wire representation as a JSON string while keeping
// the decoded value in mutable memory that callers can explicitly clear.
type Secret []byte

func (secret Secret) MarshalJSON() ([]byte, error) { return json.Marshal(string(secret)) }

func (secret *Secret) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	clear(*secret)
	*secret = append((*secret)[:0], value...)
	return nil
}

type ActivationCommandResult struct {
	ServerID string `json:"server_id"`
	State    string `json:"state"`
}

// MobilePairingBeginPayload is the payload for CmdMobilePairingBegin.
// ServerID selects which activated server profile to pair against - a
// daemon may hold several.
type MobilePairingBeginPayload struct {
	ServerID string `json:"server_id"`
}

// MobilePairingBeginResult is the result for CmdMobilePairingBegin. Field
// names are fixed by the already-shipped Android client (MainActivity.kt /
// androidbind.PairJSON expect exactly address/host_public_key/token) - do
// not rename. Token/ExpiresAt come from the server's MOBILE_PAIRING ticket
// exchange; Address/HostPublicKey are filled in locally by the daemon from
// its own already-activated clientprofile.Profile, since the server-side
// result carries neither.
type MobilePairingBeginResult struct {
	Token         string `json:"token"`
	ExpiresAt     string `json:"expires_at"`
	Address       string `json:"address"`
	HostPublicKey string `json:"host_public_key"`
}

type RepoCreateRequestPayload struct {
	ServerID    string `json:"server_id"`
	DisplayName string `json:"display_name"`
	LocalPath   string `json:"local_path"`
}

type RepoAttachIntentPayload struct {
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	LocalPath string `json:"local_path"`
}

type RepoAttachApprovePayload struct {
	OperationID string `json:"operation_id"`
	ServerID    string `json:"server_id"`
	RepoID      string `json:"repo_id"`
}

type RepoRelocatePayload struct {
	ServerID     string `json:"server_id"`
	RepoID       string `json:"repo_id"`
	NewLocalPath string `json:"new_local_path"`
}

type RepoLocatePayload struct {
	ServerID          string `json:"server_id"`
	RepoID            string `json:"repo_id"`
	ExistingLocalPath string `json:"existing_local_path"`
}

type RepoDetachPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
}

// RepoLoadDumpPayload triggers LOAD_REPOSITORY_DUMP for a repository the
// client already has attached (create + carrier commit already done through
// the normal repo-creation flow). ApplyCurrentIgnorePolicy/KeepLastRevisions
// mirror the ticket's own options 1:1 - the daemon forwards them verbatim,
// it does not interpret them.
type RepoLoadDumpPayload struct {
	ServerID                 string `json:"server_id"`
	RepoID                   string `json:"repo_id"`
	ApplyCurrentIgnorePolicy bool   `json:"apply_current_ignore_policy,omitempty"`
	KeepLastRevisions        *int   `json:"keep_last_revisions,omitempty"`
}

type RepoGrantAccessPayload struct {
	ServerID         string `json:"server_id"`
	RepoID           string `json:"repo_id"`
	RecipientRealmID string `json:"recipient_realm_id"`
	Access           string `json:"access"`
}

// RepoSetEditingPolicyPayload switches a repository between plain
// merge-on-commit and edit passports. Owner-only, enforced server-side.
type RepoSetEditingPolicyPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Policy   string `json:"policy"` // "" or "free" for the default, "lock_required" to opt in
}

type RepoSetEditingPolicyResult struct {
	RepoID string `json:"repo_id"`
	Policy string `json:"policy"`
}

type RepoRevokeAccessPayload struct {
	ServerID         string `json:"server_id"`
	RepoID           string `json:"repo_id"`
	RecipientRealmID string `json:"recipient_realm_id"`
}

type RealmGrantResult struct {
	RepoID           string `json:"repo_id"`
	RecipientRealmID string `json:"recipient_realm_id"`
	Access           string `json:"access,omitempty"`
	State            string `json:"state"`
}

type PublicShareObject struct {
	PublicID    string `json:"public_id"`
	RepoPath    string `json:"repo_path"`
	DisplayName string `json:"display_name"`
}

type PublicShareDeclaration struct {
	RepoID       string              `json:"repo_id"`
	SourceRoot   string              `json:"source_root"`
	Slug         string              `json:"slug"`
	Recipients   []string            `json:"recipients,omitempty"`
	PasswordHash string              `json:"password_hash,omitempty"`
	DoNotFollow  *int64              `json:"do-not-follow,omitempty"`
	Objects      []PublicShareObject `json:"object_map"`
}

type PublicShareListPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
}

type PublicShareCreatePayload struct {
	ServerID string `json:"server_id"`
	PublicShareDeclaration
}

type PublicShareUpdatePayload struct {
	ServerID     string `json:"server_id"`
	ChannelID    string `json:"channel_id"`
	KeepPassword bool   `json:"keep_password,omitempty"`
	PublicShareDeclaration
}

type PublicShareChannelPayload struct {
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	ChannelID string `json:"channel_id"`
}

type PublicShareSummary struct {
	ChannelID         string              `json:"channel_id"`
	RepoID            string              `json:"repo_id"`
	Alias             string              `json:"alias"`
	Slug              string              `json:"slug"`
	State             string              `json:"state"`
	SourceRoot        string              `json:"source_root"`
	Recipients        []string            `json:"recipients,omitempty"`
	PasswordProtected bool                `json:"password_protected,omitempty"`
	DoNotFollow       *int64              `json:"do-not-follow,omitempty"`
	Objects           []PublicShareObject `json:"object_map"`
	UpdatedAt         string              `json:"updated_at"`
}

type PublicShareListResult struct {
	Shares []PublicShareSummary `json:"shares"`
}

type PublicShareResult struct {
	ChannelID           string `json:"channel_id"`
	Alias               string `json:"alias"`
	Slug                string `json:"slug"`
	State               string `json:"state"`
	RecipientDeliveries int    `json:"recipient_deliveries,omitempty"`
}

type SystemLifecycleResult struct {
	Action string `json:"action"` // restart | shutdown
}

type RepoLifecycleStatusPayload struct {
	OperationID string `json:"operation_id"`
}

type RepoLifecycleResult struct {
	OperationID      string `json:"operation_id"`
	ServerID         string `json:"server_id"`
	RepoID           string `json:"repo_id,omitempty"`
	LocalPath        string `json:"local_path"`
	PendingLocalPath string `json:"pending_local_path,omitempty"`
	State            string `json:"state"`
	LastError        string `json:"last_error,omitempty"`
}

type RepoActivityPayload struct {
	Limit int `json:"limit,omitempty"`
}

type ActivityRecord struct {
	RepoID     string `json:"repo_id"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Stage      string `json:"stage"`
	DetectedAt string `json:"detected_at"`
	UpdatedAt  string `json:"updated_at"`
	Revision   int64  `json:"revision,omitempty"`
	ErrorID    string `json:"error_id,omitempty"`
}

type RepoActivityResult struct {
	Entries []ActivityRecord `json:"entries"`
}

// RepoListResult is the result for CmdRepoList.
type RepoListResult struct {
	Repos []RepoSummary `json:"repos"`
}

// RepoSummary is a minimal descriptor used in RepoListResult.
type RepoSummary struct {
	ID               string `json:"id"`
	ServerID         string `json:"server_id"`
	DisplayName      string `json:"display_name"`
	Attached         bool   `json:"attached"`
	Access           string `json:"access"`
	URL              string `json:"url"`
	LocalPath        string `json:"local_path"`
	State            string `json:"state"`
	OwnerRealmID     string `json:"owner_realm_id,omitempty"`
	AttachmentPolicy string `json:"attachment_policy"`
}

// RepoIDPayload is used by commands that target a specific repo (CmdRepoPause, etc.).
type RepoIDPayload struct {
	// RepoID is carried in the Request envelope; this payload is empty for most repo commands.
}

// ConflictGetPayload is the payload for CmdConflictGet.
type ConflictGetPayload struct {
	DecisionID string `json:"decision_id"`
}

// ConflictListResult is the result for CmdConflictList.
type ConflictListResult struct {
	Conflicts []Decision `json:"conflicts"`
}

// ConflictDecidePayload is the payload for CmdConflictDecide (§10).
type ConflictDecidePayload struct {
	DecisionID string `json:"decision_id"`
	Choice     string `json:"choice"`
}

// NoticeAckPayload is the payload for CmdNoticeAck.
type NoticeAckPayload struct {
	NoticeID string `json:"notice_id"`
}

// Notice is a single outbound notification record.
type Notice struct {
	ID        string `json:"id"`
	RepoID    string `json:"repo_id,omitempty"`
	CreatedAt string `json:"created_at"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Acked     bool   `json:"acked"`
}

// NoticeListResult is the result for CmdNoticeList.
type NoticeListResult struct {
	Notices []Notice `json:"notices"`
}

// ErrorListPayload optionally filters by repo.
type ErrorListPayload struct {
	RepoID string `json:"repo_id,omitempty"`
	Limit  int    `json:"limit,omitempty"` // 0 = daemon default (20)
}

// ErrorRecord is one entry from the structured error log.
type ErrorRecord struct {
	ID       string `json:"id"`
	TS       string `json:"ts"`
	RepoID   string `json:"repo_id,omitempty"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Hint     string `json:"hint"`
	Msg      string `json:"msg"`
	Details  string `json:"details,omitempty"`
}

// ErrorListResult is the result for CmdErrorList. Errors are ordered globally
// by timestamp from oldest to newest, including when multiple repos are queried.
type ErrorListResult struct {
	Errors []ErrorRecord `json:"errors"`
}

// ErrorGetPayload is the payload for CmdErrorGet.
type ErrorGetPayload struct {
	ID string `json:"id"`
}

// RepoLockPayload is the payload for CmdRepoLock and CmdRepoUnlock.
// Paths must be absolute filesystem paths; the daemon resolves them relative to the WC.
type RepoLockPayload struct {
	Paths []string `json:"paths"`
}

// LockResult is the result for CmdRepoLock and CmdRepoUnlock.
type LockResult struct {
	Output string `json:"output"` // raw SVN output for display
}

// RepoReservationListPayload scopes a live lock inventory to one activated
// server.  The daemon returns locks only from locally attached working copies;
// it never claims to be a server-administrative inventory of unseen repos.
type RepoReservationListPayload struct {
	ServerID string `json:"server_id"`
}

// Reservation describes one authoritative SVN lock observed by the daemon.
// Token is an opaque fencing value: callers must return it unchanged when
// asking to release a row, so a stale dialog cannot unlock a later lock.
type Reservation struct {
	RepoID      string `json:"repo_id"`
	WorkingCopy string `json:"working_copy"`
	Path        string `json:"path"` // repository-relative, slash-separated
	Token       string `json:"token"`
	// OwnerID is daemon-internal data obtained from SVN and is never encoded
	// on the local GUI IPC. The presentation contract exposes OwnerLabel only.
	OwnerID        string `json:"-"`
	OwnerLabel     string `json:"owner_label,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	CanRelease     bool   `json:"can_release"`
	LocalChanges   bool   `json:"local_changes"`
	ActivePassport bool   `json:"active_passport"`
}

// RepoReservationListResult is sorted by working copy and path by the daemon.
type RepoReservationListResult struct {
	ServerID     string        `json:"server_id"`
	Reservations []Reservation `json:"reservations"`
}

// RepoReservationReleasePayload identifies a row returned by
// repo.reservation_list.  Path is relative to the selected working copy;
// ExpectedToken prevents a stale client from releasing a newer reservation.
// ConfirmRisk records the user's explicit acknowledgement of known local
// changes or an active FileES edit passport.
type RepoReservationReleasePayload struct {
	ServerID      string `json:"server_id"`
	RepoID        string `json:"repo_id"`
	Path          string `json:"path"`
	ExpectedToken string `json:"expected_token"`
	ConfirmRisk   bool   `json:"confirm_risk,omitempty"`
}
