package contract

import (
	"encoding/json"
	"time"

	"filees/pkg/realmbranding"
)

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
	CmdActivationBegin         = "activation.begin"
	CmdActivationFinish        = "activation.finish"
	CmdActivationPending       = "activation.pending"
	CmdActivationResume        = "activation.resume"
	CmdRealmAliasClaim         = "realm.alias_claim"
	CmdRealmGrantRecipients    = "realm.grant_recipients"
	CmdRealmSetVisibility      = "realm.set_visibility"
	CmdRealmPublicBrandingGet  = "realm.public_branding_get"
	CmdRealmPublicBrandingSet  = "realm.public_branding_set"
	CmdServerDetach            = "server.detach"
	CmdServerSetSessionTimeout = "server.set_session_timeout" // how long one send or fetch may run on this server
	CmdRealmRemoveBegin        = "realm.remove_begin"
	CmdRealmRemoveConfirm      = "realm.remove_confirm"
	CmdRecoveryDownload        = "recovery.download"

	// Mobile pairing (Phase 2c): daemon mints a MOBILE_PAIRING token through
	// its own already-authenticated control-plane channel; the tray hands
	// the result to a separate helper process that renders it as a QR code.
	CmdMobilePairingBegin = "mobile_pairing.begin"

	// Repos
	CmdRepoList                = "repo.list"                    // list all configured repos
	CmdRepoStatus              = "repo.status"                  // snapshot of one repo
	CmdRepoPause               = "repo.pause"                   // suspend automatic operations
	CmdRepoResume              = "repo.resume"                  // resume after pause
	CmdRepoSyncNow             = "repo.sync_now"                // request immediate poll/update
	CmdRepoPublish             = "repo.publish"                 // request immediate commit of pending changes
	CmdRepoCreateRequest       = "repo.create_request"          // persist intent; server work is a later stage
	CmdRepoAttachIntent        = "repo.attach_intent"           // persist local path choice; no checkout yet
	CmdRepoAttachApprove       = "repo.attach_approve"          // approve the persisted intent and start checkout
	CmdRepoRelocate            = "repo.relocate"                // approve relocation of an attached working copy
	CmdRepoLocate              = "repo.locate"                  // rebind an attachment to an existing moved working copy
	CmdRepoLoadDump            = "repo.load_dump"               // load a user-supplied dump into a fresh, single-carrier-commit repo
	CmdRepoGrantAccess         = "repo.grant_access"            // grant r/rw access to a visible foreign realm
	CmdRepoRevokeAccess        = "repo.revoke_access"           // revoke a realm grant without deleting local data
	CmdRepoSetEditingPolicy    = "repo.set_editing_policy"      // owner-only: switch a repository between free and lock_required editing
	CmdRepoPublicShareList     = "repo.public_share_list"       // list owned public distribution channels
	CmdRepoPublicShareCreate   = "repo.public_share_create"     // create an owned public distribution channel
	CmdRepoPublicShareUpdate   = "repo.public_share_update"     // update one owned active channel
	CmdRepoPublicShareRevoke   = "repo.public_share_revoke"     // revoke access while retaining the channel record
	CmdRepoPublicShareDelete   = "repo.public_share_delete"     // delete policy while retaining the address tombstone
	CmdRepoPublicShareListAll  = "repo.public_share_list_all"   // cached cross-repo aggregate of owned public shares (all servers)
	CmdRepoUploadChannelList   = "repo.upload_channel_list"     // list owned upload shelves for one authority repo
	CmdRepoUploadChannelCreate = "repo.upload_channel_create"   // create an owned closed upload shelf
	CmdRepoUploadChannelUpdate = "repo.upload_channel_update"   // update recipients of one owned active shelf
	CmdRepoUploadChannelRevoke = "repo.upload_channel_revoke"   // revoke intake while retaining the channel record
	CmdRepoUploadChannelDelete = "repo.upload_channel_delete"   // delete policy while retaining the address tombstone
	CmdRepoQuarantineList      = "repo.quarantine_list"         // owner listing of AV rejects
	CmdRepoQuarantineHide      = "repo.quarantine_hide"         // hide one reject in the manifest
	CmdRepoQuarantineFetch     = "repo.quarantine_fetch"        // copy payload from the waiting room
	CmdRepoDetach              = "repo.detach"                  // detach one local working copy, preserving user data
	CmdRepoDelete              = "repo.delete"                  // delete an owned server repository; detach and clean a local WC when present
	CmdRepoLifecycleStatus     = "repo.lifecycle_status"        // poll outcome of a create/attach/relocate operation by ID
	CmdRepoLifecycleRepair     = "repo.lifecycle_repair"        // retry or locally abandon one stuck durable operation
	CmdRepoActivity            = "repo.activity"                // global recent synchronization activity snapshot
	CmdRepoLock                = "repo.lock"                    // acquire SVN lock on one or more paths
	CmdRepoUnlock              = "repo.unlock"                  // release SVN lock on one or more paths
	CmdRepoReservationList     = "repo.reservation_list"        // list live locks in this client's working copies for one server
	CmdRepoReservationRelease  = "repo.reservation_release"     // safely release one listed lock
	CmdLockReleaseRequest      = "lock.release-request"         // ask the holder of one observed SVN lock to release it
	CmdLockReleaseDismiss      = "lock.release-request-dismiss" // holder acknowledges without releasing the lock
	CmdLockReleaseAccept       = "lock.release-request-accept"  // holder accepts, then the daemon releases the fenced lock

	// Conflicts and user decisions
	CmdConflictList   = "conflict.list"   // list pending conflicts / interactions
	CmdConflictGet    = "conflict.get"    // details of one conflict
	CmdConflictDecide = "conflict.decide" // submit user decision

	// Notices (outbound notifications)
	CmdNoticeList = "notice.list" // list unread and recent acknowledged notices
	CmdNoticeAck  = "notice.ack"  // mark notice as acknowledged

	// Structured error log
	CmdErrorList = "error.list" // recent error records
	CmdErrorGet  = "error.get"  // single error by ID

	// Event streaming
	CmdEventsSubscribe = "events.subscribe" // switch connection to event push mode

	// Whale operations are editor-neutral intents. GUI, CAD and other 3rd
	// party plugins all observe and mutate the same daemon-owned actor state.
	CmdWhaleList       = "whale.list"
	CmdWhaleGet        = "whale.get"
	CmdWhalePutBegin   = "whale.put_begin"
	CmdWhaleGetBegin   = "whale.get_begin"
	CmdWhaleGetConfirm = "whale.get_confirm"
	CmdWhaleRetry      = "whale.retry"
	CmdWhaleCancel     = "whale.cancel"
)

// Capability constants — the daemon advertises which commands are active (§12).
// GUI shows only capabilities declared in HelloResult.Capabilities.
// Only list commands that are actually implemented.
const (
	CapEventsSubscribe         = "events.subscribe"
	CapRepoLock                = "repo.lock"
	CapRepoUnlock              = "repo.unlock"
	CapRepoReservationList     = "repo.reservation_list"
	CapRepoReservationRelease  = "repo.reservation_release"
	CapLockReleaseRequest      = "lock.release-request"
	CapLockReleaseDismiss      = "lock.release-request-dismiss"
	CapLockReleaseAccept       = "lock.release-request-accept"
	CapErrorList               = "error.list"
	CapActivationBegin         = "activation.begin"
	CapActivationFinish        = "activation.finish"
	CapActivationPending       = "activation.pending"
	CapActivationResume        = "activation.resume"
	CapRealmAliasClaim         = "realm.alias_claim"
	CapRealmGrantRecipients    = "realm.grant_recipients"
	CapRealmSetVisibility      = "realm.set_visibility"
	CapRealmPublicBrandingGet  = "realm.public_branding_get"
	CapRealmPublicBrandingSet  = "realm.public_branding_set"
	CapServerDetach            = "server.detach"
	CapServerSetSessionTimeout = "server.set_session_timeout"
	CapRealmRemoveBegin        = "realm.remove_begin"
	CapRealmRemoveConfirm      = "realm.remove_confirm"
	CapRecoveryDownload        = "recovery.download"
	CapMobilePairingBegin      = "mobile_pairing.begin"
	CapRepoCreateRequest       = "repo.create_request"
	CapRepoAttachIntent        = "repo.attach_intent"
	CapRepoAttachApprove       = "repo.attach_approve"
	CapRepoRelocate            = "repo.relocate"
	CapRepoLocate              = "repo.locate"
	CapRepoLoadDump            = "repo.load_dump"
	CapRepoGrantAccess         = "repo.grant_access"
	CapRepoRevokeAccess        = "repo.revoke_access"
	CapRepoSetEditingPolicy    = "repo.set_editing_policy"
	CapRepoPublicShareList     = "repo.public_share_list"
	CapRepoPublicShareCreate   = "repo.public_share_create"
	CapRepoPublicShareUpdate   = "repo.public_share_update"
	CapRepoPublicShareRevoke   = "repo.public_share_revoke"
	CapRepoPublicShareDelete   = "repo.public_share_delete"
	CapRepoUploadChannelList   = "repo.upload_channel_list"
	CapRepoUploadChannelCreate = "repo.upload_channel_create"
	CapRepoUploadChannelUpdate = "repo.upload_channel_update"
	CapRepoUploadChannelRevoke = "repo.upload_channel_revoke"
	CapRepoUploadChannelDelete = "repo.upload_channel_delete"
	CapRepoQuarantineList      = "repo.quarantine_list"
	CapRepoQuarantineHide      = "repo.quarantine_hide"
	CapRepoQuarantineFetch     = "repo.quarantine_fetch"
	CapRepoDetach              = "repo.detach"
	CapRepoDelete              = "repo.delete"
	CapRepoLifecycleStatus     = "repo.lifecycle_status"
	CapRepoLifecycleRepair     = "repo.lifecycle_repair"
	CapRepoActivity            = "repo.activity"
	CapRepoPublish             = "repo.publish"
	CapNoticeList              = "notice.list"
	CapNoticeAck               = "notice.ack"
	CapSystemRestart           = "system.restart"
	CapSystemShutdown          = "system.shutdown"
	CapWhaleList               = "whale.list"
	CapWhaleGet                = "whale.get"
	CapWhalePutBegin           = "whale.put_begin"
	CapWhaleGetBegin           = "whale.get_begin"
	CapWhaleGetConfirm         = "whale.get_confirm"
	CapWhaleRetry              = "whale.retry"
	CapWhaleCancel             = "whale.cancel"

	// Update capabilities are advertised only after the daemon wires a signed
	// release checker and transactional platform installer.
	CapUpdateStatus = "update.status"
	CapUpdatePlan   = "update.plan"
	CapUpdateApply  = "update.apply"

	// Not yet implemented — defined for future use but NOT in AllCapabilities.
	CapRepoPause      = "repo.pause"
	CapRepoSyncNow    = "repo.sync_now"
	CapConflictDecide = "conflict.decide"
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
	CapActivationPending,
	CapActivationResume,
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
	CapRepoLifecycleRepair,
	CapRepoPublish,
	CapNoticeList,
	CapNoticeAck,
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
	// Detachments are the relationships that ended, beside the ones that
	// exist. They are a separate list rather than a flag on an activation
	// because after a detachment there is no activation left to carry it:
	// detaching removes the client profile, and a revoked client loses its
	// server view. A flag can only describe something still present.
	Detachments         []Detachment         `json:"detachments,omitempty"`
	LockReleaseRequests []LockReleaseRequest `json:"lock_release_requests,omitempty"`
	Recoveries          []RecoveryStatus     `json:"recoveries,omitempty"`
	Update              *UpdateStatus        `json:"update,omitempty"`
}

// Detachment reports one ended relationship with a server, carrying the
// moment it ended rather than the fact that it has.
//
// Everything here is what the client knew at that moment, copied in then. The
// server's own names are unreachable afterwards by definition, so a reader
// given only an ID would be looking at a UID next to a date - which is exactly
// the failure recorded as A12 in the seam register.
type Detachment struct {
	ServerID    string `json:"server_id"`
	DisplayName string `json:"display_name,omitempty"`
	Address     string `json:"address,omitempty"`
	// Cause is "self" when the owner detached here, "revoked" when the server
	// refused this client. They reach the same end state by opposite routes
	// and need opposite cures - one is finished, the other needs the client
	// activated again - so the reader must never be told the wrong one.
	Cause string `json:"cause"`
	// At is RFC3339. For "revoked" it is when the daemon first noticed, never
	// when the decision was made on the server, and the wording that reaches
	// the reader must not claim more precision than that.
	At string `json:"at"`
	// WorkingCopies are the local paths that belonged to this server. The
	// files stay on this disk after a detachment, and where they are is the
	// only question left with a useful answer.
	WorkingCopies []string `json:"working_copies,omitempty"`
	// ReattachedAt is set, in RFC3339, once the client is one of this server's
	// own again. The record is kept rather than dropped because its two
	// readers want opposite things: a panel describing how things stand must
	// not list a server that is back, while a chronology of what happened must
	// not delete an entry because circumstances later changed.
	ReattachedAt string `json:"reattached_at,omitempty"`
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
	Channel          string `json:"channel,omitempty"`
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
	// SessionTimeoutMin is how long one send or fetch may run on this
	// server, in minutes. Zero means the default (30).
	SessionTimeoutMin int `json:"session_timeout_min,omitempty"`

	// The next five describe the projection lane for this server, and only
	// that lane. They answer "how old is what we are showing", which nothing
	// answered before: the interface had the GUI-to-daemon link and called it
	// the projection, so a view frozen for ten days rendered as current.
	//
	// They are deliberately not a health verdict. The reservation emission
	// answers its own question per repository and the same server can be fresh
	// there while refused here, because the two lanes are different SSH
	// commands - so a presentation layer must read both rather than collapse
	// them into one lamp.
	ViewGeneration   int64  `json:"view_generation,omitempty"`
	ViewGeneratedAt  string `json:"view_generated_at,omitempty"`
	ViewSyncedAt     string `json:"view_synced_at,omitempty"`
	ViewSyncError    string `json:"view_sync_error,omitempty"`
	ViewSyncFailures int    `json:"view_sync_failures,omitempty"`

	// What the server itself says about producing this client's view, as
	// opposed to what we managed to fetch. The two answer different questions
	// and the difference between them is the diagnosis: equal generations mean
	// we are current with the producer, a higher server generation means we
	// are behind, and both old means the server has stopped producing.
	//
	// Only the last of those is invisible from the client alone, which is why
	// it has to be asked for rather than inferred from age.
	ServerViewGeneration int64  `json:"server_view_generation,omitempty"`
	ServerViewProducedAt string `json:"server_view_produced_at,omitempty"`

	// Detached means this server has refused this client: it is no longer one
	// of its own, normally because somebody deactivated it here on purpose.
	//
	// It is not a freshness field and not a failure count. Those describe how
	// well we are keeping up with a server that still knows us; this says the
	// relationship is over until the client is activated again. Presenting it
	// as a stale or unreachable server sends the reader to wait for something
	// that will never arrive, and to doubt a server that is working exactly as
	// instructed.
	Detached bool `json:"detached,omitempty"`
}

type ServerSetSessionTimeoutPayload struct {
	ServerID string `json:"server_id"`
	Minutes  int    `json:"minutes"`
}

type ServerSetSessionTimeoutResult struct {
	ServerID string `json:"server_id"`
	Minutes  int    `json:"minutes"`
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

type ActivationPendingPayload struct {
	StateRoot string `json:"state_root"`
}

type ActivationResumePayload struct {
	ServerID       string `json:"server_id"`
	ServerAddress  string `json:"server_address"`
	KnownHostsPath string `json:"known_hosts_path"`
	StateRoot      string `json:"state_root"`
	RemotePort     int    `json:"remote_port"`
}

type ActivationPendingResult struct {
	Targets []ActivationTarget `json:"targets"`
}

type ActivationTarget struct {
	ServerID string `json:"server_id"`
	Address  string `json:"address"`
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

type RealmPublicBrandingGetPayload struct {
	ServerID string `json:"server_id"`
}

type RealmPublicBrandingSetPayload struct {
	ServerID string                 `json:"server_id"`
	Branding realmbranding.Branding `json:"branding"`
}

type RealmPublicBrandingResult struct {
	Branding realmbranding.Branding `json:"branding"`
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
	Size        *int64 `json:"size,omitempty"`
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
	ServerID          string              `json:"server_id,omitempty"`
	RepoID            string              `json:"repo_id"`
	RepoDisplayName   string              `json:"repo_display_name,omitempty"`
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

// UploadChannelDeclaration is the owner-supplied shelf body. Kind is omitted:
// the daemon and server treat absence as shelf. RequireOTP asks the contributor
// for a mailbox and a one-time mail code before the file form.
type UploadChannelDeclaration struct {
	AuthorityRepoID string   `json:"authority_repo_id"`
	Slug            string   `json:"slug"`
	Kind            string   `json:"kind,omitempty"`
	Recipients      []string `json:"recipients"`
	RequireOTP      bool     `json:"require_otp,omitempty"`
}

type UploadChannelListPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
}

type UploadChannelCreatePayload struct {
	ServerID string `json:"server_id"`
	UploadChannelDeclaration
}

type UploadChannelUpdatePayload struct {
	ServerID  string `json:"server_id"`
	ChannelID string `json:"channel_id"`
	UploadChannelDeclaration
}

type UploadChannelChannelPayload struct {
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	ChannelID string `json:"channel_id"`
}

type UploadChannelSummary struct {
	ChannelID       string   `json:"channel_id"`
	AuthorityRepoID string   `json:"authority_repo_id"`
	UploadRepoID    string   `json:"upload_repo_id"`
	Alias           string   `json:"alias"`
	Slug            string   `json:"slug"`
	Kind            string   `json:"kind,omitempty"`
	State           string   `json:"state"`
	Recipients      []string `json:"recipients"`
	RequireOTP      bool     `json:"require_otp,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
}

type UploadChannelListResult struct {
	Channels             []UploadChannelSummary `json:"channels"`
	QuarantineAlias      string                 `json:"quarantine_alias,omitempty"`
	QuarantineSlug       string                 `json:"quarantine_slug,omitempty"`
	QuarantineInvitation string                 `json:"quarantine_invitation,omitempty"`
}

type UploadChannelResult struct {
	ChannelID            string `json:"channel_id"`
	Alias                string `json:"alias"`
	Slug                 string `json:"slug"`
	State                string `json:"state"`
	UploadRepoID         string `json:"upload_repo_id,omitempty"`
	RecipientDeliveries  int    `json:"recipient_deliveries,omitempty"`
	QuarantineAlias      string `json:"quarantine_alias,omitempty"`
	QuarantineSlug       string `json:"quarantine_slug,omitempty"`
	QuarantineInvitation string `json:"quarantine_invitation,omitempty"`
}

type QuarantineListPayload struct {
	ServerID string `json:"server_id"`
}
type QuarantineItemPayload struct {
	ServerID string `json:"server_id"`
	UploadID string `json:"upload_id"`
}
type QuarantineItem struct {
	UploadID       string `json:"upload_id"`
	OriginalName   string `json:"original_name"`
	Size           int64  `json:"size"`
	AVVerdict      string `json:"av_verdict,omitempty"`
	ReceivedAt     string `json:"received_at"`
	RemainingHours int    `json:"remaining_hours"`
}
type QuarantinePurged struct {
	UploadID     string `json:"upload_id"`
	OriginalName string `json:"original_name"`
	PurgedAt     string `json:"purged_at"`
}
type QuarantineListResult struct {
	Items   []QuarantineItem   `json:"items"`
	Purged  []QuarantinePurged `json:"purged,omitempty"`
	Message string             `json:"message,omitempty"`
}
type QuarantineHideResult struct {
	UploadID string `json:"upload_id"`
}
type QuarantineFetchResult struct {
	UploadID       string `json:"upload_id"`
	OriginalName   string `json:"original_name"`
	Payload        []byte `json:"payload"`
	RemainingHours int    `json:"remaining_hours"`
}

type SystemLifecycleResult struct {
	Action string `json:"action"` // restart | shutdown
}

type RepoLifecycleStatusPayload struct {
	OperationID string `json:"operation_id"`
}

type RepoLifecycleRepairPayload struct {
	OperationID string `json:"operation_id"`
	ServerID    string `json:"server_id"`
	RepoID      string `json:"repo_id"`
	Strategy    string `json:"strategy"` // retry | abandon
}

type RepoLifecycleResult struct {
	OperationID           string `json:"operation_id"`
	ServerID              string `json:"server_id"`
	RepoID                string `json:"repo_id,omitempty"`
	LocalPath             string `json:"local_path"`
	PendingLocalPath      string `json:"pending_local_path,omitempty"`
	State                 string `json:"state"`
	LastError             string `json:"last_error,omitempty"`
	ServerDeleteCompleted bool   `json:"server_delete_completed,omitempty"`
	RetainUntil           string `json:"retain_until,omitempty"`
	RecoveryPrepared      bool   `json:"recovery_prepared,omitempty"`
	RecoveryKitPath       string `json:"recovery_kit_path,omitempty"`
	LocalCleanupCompleted bool   `json:"local_cleanup_completed,omitempty"`
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
	Size       *int64 `json:"size,omitempty"`
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
	ID                   string `json:"id"`
	ServerID             string `json:"server_id"`
	DisplayName          string `json:"display_name"`
	Attached             bool   `json:"attached"`
	Access               string `json:"access"`
	URL                  string `json:"url"`
	LocalPath            string `json:"local_path"`
	State                string `json:"state"`
	OwnerRealmID         string `json:"owner_realm_id,omitempty"`
	AttachmentPolicy     string `json:"attachment_policy"`
	ServerDeleted        bool   `json:"server_deleted,omitempty"`
	LocalCleanupPending  bool   `json:"local_cleanup_pending,omitempty"`
	RetainUntil          string `json:"retain_until,omitempty"`
	RecoveryOperationID  string `json:"recovery_operation_id,omitempty"`
	RecoveryAvailable    bool   `json:"recovery_available,omitempty"`
	RecoveryPending      bool   `json:"recovery_pending,omitempty"`
	CleanupError         string `json:"cleanup_error,omitempty"`
	Purpose              string `json:"purpose,omitempty"`
	LifecycleOperationID string `json:"lifecycle_operation_id,omitempty"`
	LifecycleError       string `json:"lifecycle_error,omitempty"`
	CanRetryLifecycle    bool   `json:"can_retry_lifecycle,omitempty"`
	CanAbandonLifecycle  bool   `json:"can_abandon_lifecycle,omitempty"`
}

// RepoPublishPayload is the required comment for a shouting commit.
type RepoPublishPayload struct {
	Comment string `json:"comment"`
}

// RepoPublishResult is the revision created by a shouting commit.
type RepoPublishResult struct {
	Revision int64 `json:"revision"`
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
	Revision  int64  `json:"revision,omitempty"`
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

// ReservationSourceState classifies one repository's contribution to a
// RepoReservationListResult. The closed set is
// fresh/stale/offline/detached/unknown;
// a repository absent from Sources never happened — every repository queried
// gets exactly one entry.
type ReservationSourceState string

const (
	// ReservationSourceFresh: the live refresh from the authoritative
	// remote server succeeded just now.
	ReservationSourceFresh ReservationSourceState = "fresh"
	// ReservationSourceStale: the live refresh failed, but the remote
	// serving-state worker replayed its last confirmed artifact.
	ReservationSourceStale ReservationSourceState = "stale"
	// ReservationSourceOffline: the desktop daemon could not reach this
	// server's state lane and is replaying its local mirror of an earlier,
	// server-confirmed emission.
	ReservationSourceOffline ReservationSourceState = "offline"
	// ReservationSourceDetached: the server was reached and refused us. This
	// client is no longer one of its own - normally because someone
	// deactivated it, which is a thing the owner does on purpose - so the
	// mirror being replayed will never be refreshed again from here.
	//
	// Deliberately not Offline. Offline says "could not reach this server",
	// and reporting a revocation that way tells the reader to wait for a
	// network that is already working. The two need opposite responses: wait,
	// versus activate this client again. Measured 2026-09-03, when a
	// deactivation the owner had asked for surfaced as an unavailable server
	// and the daemon knocked 234 times.
	ReservationSourceDetached ReservationSourceState = "detached"
	// ReservationSourceUnknown: neither a live answer nor any prior
	// artifact exists for this repository. Never treat this as a
	// confirmed zero — no reservation rows for this repo are trustworthy.
	ReservationSourceUnknown ReservationSourceState = "unknown"
)

// ReservationSource is one repository's freshness classification within a
// RepoReservationListResult. AsOf/Generation are meaningful for Fresh,
// Stale and Offline; both are zero for Unknown — never faked to look
// otherwise.
type ReservationSource struct {
	RepoID     string                 `json:"repo_id"`
	State      ReservationSourceState `json:"state"`
	AsOf       time.Time              `json:"as_of,omitempty"`
	Generation string                 `json:"generation,omitempty"`
}

// RepoReservationListResult is sorted by working copy and path by the
// daemon. Reservations is the union of every non-Unknown source's rows;
// Sources carries one entry per repository queried on this server,
// regardless of outcome, so a caller can never mistake "some repositories
// on this server are Unknown" for "the whole server is known and fine" —
// see concepts/RESERVATION_LISTING_RESILIENCE_CONCEPT.md §1.4. There is
// deliberately no single aggregate Stale/AsOf/Generation at this level:
// with more than one repository per server those numbers have no one
// honest meaning, so callers must read Sources instead of asking this
// struct to summarize them.
type RepoReservationListResult struct {
	ServerID     string              `json:"server_id"`
	Reservations []Reservation       `json:"reservations"`
	Sources      []ReservationSource `json:"sources"`
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

// LockReleaseRequest is the daemon-to-GUI projection of one server-owned
// request. ObservedLockID remains opaque and is returned unchanged to the
// server; Role determines which actions this client may present.
type LockReleaseRequest struct {
	RequestID              string `json:"request_id"`
	ServerID               string `json:"server_id"`
	RepoID                 string `json:"repo_id"`
	Path                   string `json:"path"`
	ObservedLockID         string `json:"observed_lock_id"`
	Role                   string `json:"role"`
	CounterpartyRealmAlias string `json:"counterparty_realm_alias,omitempty"`
	State                  string `json:"state"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
	ExpiresAt              string `json:"expires_at"`
}

type LockReleaseRequestPayload struct {
	ServerID       string `json:"server_id"`
	RepoID         string `json:"repo_id"`
	Path           string `json:"path"`
	ObservedLockID string `json:"observed_lock_id"`
}

type LockReleaseDecisionPayload struct {
	ServerID  string `json:"server_id"`
	RequestID string `json:"request_id"`
}

type WhaleIdentity struct {
	LogicalRepoID string `json:"logical_repo_id"`
	LogicalPath   string `json:"logical_path"`
	GenerationID  string `json:"generation_id"`
	ExpectedSize  int64  `json:"expected_size"`
	SHA256        string `json:"sha256"`
}

// WhaleOperation is the complete renderer-facing projection. No GUI-local
// transition is authoritative; reconnecting clients rebuild from this value.
type WhaleOperation struct {
	OperationID       string        `json:"operation_id"`
	ServerID          string        `json:"server_id"`
	Direction         string        `json:"direction"`
	Identity          WhaleIdentity `json:"identity"`
	Revision          int64         `json:"revision,omitempty"`
	SourcePath        string        `json:"source_path,omitempty"`
	SpoolRoot         string        `json:"spool_root,omitempty"`
	SpoolVolumeID     string        `json:"spool_volume_id,omitempty"`
	SpoolDeviceID     string        `json:"spool_device_id,omitempty"`
	ReservedBytes     int64         `json:"reserved_bytes,omitempty"`
	DestinationPath   string        `json:"destination_path,omitempty"`
	State             string        `json:"state"`
	BytesHave         int64         `json:"bytes_have"`
	PublishedRevision int64         `json:"published_revision,omitempty"`
	LastError         string        `json:"last_error,omitempty"`
	CreatedAt         string        `json:"created_at"`
	UpdatedAt         string        `json:"updated_at"`
}

type WhaleListResult struct {
	Operations []WhaleOperation `json:"operations"`
}

type WhaleOperationPayload struct {
	OperationID string `json:"operation_id"`
}

type WhaleCancelPayload struct {
	OperationID   string `json:"operation_id"`
	RemovePayload bool   `json:"remove_payload,omitempty"`
}

type WhalePutBeginPayload struct {
	ServerID    string `json:"server_id"`
	RepoID      string `json:"repo_id"`
	LogicalPath string `json:"logical_path"`
	SourcePath  string `json:"source_path"`
}

type WhaleGetBeginPayload struct {
	ServerID    string `json:"server_id"`
	RepoID      string `json:"repo_id"`
	LogicalPath string `json:"logical_path"`
	// Generation fields are optional. Without them the actor performs a
	// metadata-only discovery at Revision before asking for confirmation.
	GenerationID    string `json:"generation_id,omitempty"`
	ExpectedSize    int64  `json:"expected_size,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	Revision        int64  `json:"revision"`
	DestinationPath string `json:"destination_path"`
}
