package app

import (
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
)

// IconState is the aggregate visual state shown in the system tray icon.
// Priority (highest → lowest): disconnected > error > offline > busy > active.
type IconState string

const (
	IconActive       IconState = "active"
	IconBusy         IconState = "busy"
	IconOffline      IconState = "offline"
	IconError        IconState = "error"
	IconShout        IconState = "shout"
	IconDisconnected IconState = "disconnected"
)

// RepoDisplayState is the presentation-level repository state. It deliberately
// hides the daemon protocol vocabulary from UI adapters.
type RepoDisplayState string

const (
	RepoDisplayActive       RepoDisplayState = "active"
	RepoDisplayBusy         RepoDisplayState = "busy"
	RepoDisplayInitializing RepoDisplayState = "initializing"
	RepoDisplayBaselining   RepoDisplayState = "baselining"
	RepoDisplayPaused       RepoDisplayState = "paused"
	RepoDisplayStopping     RepoDisplayState = "stopping"
	RepoDisplayOffline      RepoDisplayState = "offline"
	RepoDisplayAttention    RepoDisplayState = "attention"
	RepoDisplayUnattached   RepoDisplayState = "unattached"
	RepoDisplayDisabled     RepoDisplayState = "disabled"
	RepoDisplayRevoked      RepoDisplayState = "revoked"
	RepoDisplayDeleted      RepoDisplayState = "deleted"
	RepoDisplayUnknown      RepoDisplayState = "unknown"
)

// RepoViewModel is the read-only presentation model for one repository.
// Constructed from RepoSummary (URL, LocalPath) + RepoStatus (live state).
type RepoViewModel struct {
	ID                   string
	DisplayName          string
	ServerID             string
	Attached             bool
	Access               string
	OwnerRealmID         string
	AttachmentPolicy     string
	EditingPolicy        string
	Purpose              string
	URL                  string
	LocalPath            string
	State                string
	Connectivity         string
	LocalRev             int64
	HeadRev              int64
	WorkingCopyBytes     int64
	WorkingCopySizeKnown bool
	Pending              contract.PendingStats
	Conflicts            int
	LastSyncAt           string
	CurrentOp            *string
	ReservationCount     int
	Cycle                contract.CycleStatus
	ServerDeleted        bool
	LocalCleanupPending  bool
	RetainUntil          string
	RecoveryOperationID  string
	RecoveryAvailable    bool
	RecoveryPending      bool
	CleanupError         string
	LifecycleOperationID string
	LifecycleError       string
	CanRetryLifecycle    bool
	CanAbandonLifecycle  bool
}

type PendingAction struct {
	ID                        string
	Kind                      string
	RepoID                    string
	ServerID                  string
	Label                     string
	Phase                     string
	StartedAt                 time.Time
	ExpectedSessionTimeoutMin int
	ExpectedRepoAttached      bool
	ExpectedRepoDetached      bool
	ExpectedRepoDeleted       bool
	ExpectedRecoveryDismissed bool
	// ExpectedLifecycleOperationID fences a repair against daemon projection:
	// the spinner remains until this exact stuck operation is no longer exposed.
	ExpectedLifecycleOperationID string
	ReservationDelta             int
	BaselineReservations         int
	BaselineReservationsKnown    bool
}

const (
	ActionRunning            = "running"
	ActionAwaitingProjection = "awaiting_projection"
)

type ServerViewModel struct {
	ID                    string
	DisplayName           string
	ClientRole            string
	RealmID               string
	RealmAlias            string
	Address               string
	ClientID              string
	SSHPort               int
	CanCreateRepositories bool
	RepositoriesReady     bool
	PendingRequiredRepos  int
	SessionTimeoutMin     int
	ReservationCount      int
	ReservationsKnown     bool
	// The view lane, kept apart from the reservation emission above because
	// the two answer different questions and the same server can be healthy on
	// one and refused on the other. Zero values mean "not measured", which a
	// presentation layer must not read as "measured and fine".
	ViewGeneration  int64
	ViewGeneratedAt string
	ViewSyncedAt    string
	ViewSyncError   string
	// Detached: this server refused us; the client is no longer one of its
	// own. Kept apart from the freshness fields because those describe keeping
	// up with a server that still knows us.
	Detached         bool
	ViewSyncFailures int
	// The server's own report about publishing for us: the only evidence that
	// separates a quiet server from an abandoned one.
	ServerViewProducedAt  string
	ReservationProjection string
	ReservationAsOf       string
	Repos                 []RepoViewModel
}

func (server ServerViewModel) CanOfferRepositoryCreation() bool {
	return server.ClientRole != contract.ClientRoleReadOnly && server.CanCreateRepositories
}

// RequiresLock reports whether this repository works through edit passports,
// which is what makes its files read-only until borrowed.
func (repo RepoViewModel) RequiresLock() bool {
	return repo.EditingPolicy == contract.EditingLockRequired
}

func (server ServerViewModel) Owns(repo RepoViewModel) bool {
	return server.RealmID != "" && repo.OwnerRealmID == server.RealmID
}

// NeedsRealmAliasClaim reports whether this realm has no alias yet.
//
// This used to also require len(server.Repos) == 0, on the theory that once
// the server projects any repository, realm membership is already
// established and a missing RealmAlias in that state must be stale/lagging
// projection data rather than a real gap - offering to "claim" it again
// risked inviting a rename. That is not actually a risk:
// pkg/repoworker/realm_alias.go's Claim is immutable once set (a different
// alias than the one already on record fails closed with
// ErrAliasImmutable/REALM_ALIAS_REJECTED; the same alias is a harmless
// no-op that still runs the view.json repair pass), so re-offering the
// action on a realm that already has an alias costs nothing worse than a
// clean rejection. Gating it away, however, left a real activation that
// failed after creating repositories but before a successful alias claim
// (confirmed live: a server-side bug briefly broke every worker session
// including alias claim, cloud.atmprojekt.pl 2026-08-31) with no way to
// reach the action at all - the exact user this exists for.
func (server ServerViewModel) NeedsRealmAliasClaim() bool {
	return server.RealmID != "" && server.RealmAlias == ""
}

// ErrorViewModel is a presentation-safe structured daemon error. Details are
// intentionally excluded from the tray model.
type ErrorViewModel struct {
	ID        string
	RepoID    string
	Timestamp string
	Code      string
	Severity  string
	Hint      string
	Message   string
}

type ActivityViewModel struct {
	RepoID, Path, Kind, Stage, UpdatedAt, ErrorID string
	Revision                                      int64
	Size                                          *int64
}

type NoticeViewModel struct {
	ID, RepoID, Title, CreatedAt string
	Revision                     int64
	Acked                        bool
}

// DetachmentViewModel is a relationship with a server that has ended, carried
// with the moment it ended rather than as a flag.
//
// It is a list of its own and never a field on ServerViewModel, because after
// a detachment there is no server view model to hang it on: r790 removes a
// detached server and its repositories from the projection entirely, and a
// self-detachment removes the client profile that produced the activation.
type DetachmentViewModel struct {
	ServerID, DisplayName, Address string
	// Cause is "self" or "revoked". Presentation must keep them apart: one is
	// finished business, the other needs the client activated again.
	Cause string
	// ReattachedAt is set once the client is one of this server's own again.
	ReattachedAt string
	// At is RFC3339, and for "revoked" it is when the daemon noticed.
	At string
	// WorkingCopies are the local paths whose files are still on this disk.
	WorkingCopies []string
}

// SelfDetached reports whether the owner did this himself.
func (d DetachmentViewModel) SelfDetached() bool { return d.Cause == "self" }

// Current reports whether this detachment still describes how things stand.
//
// The "recently detached" panel shows only current ones, because a server that
// is back belongs in the projection rather than in a list of endings. The
// journal shows every one of them, because the detachment happened and a later
// re-activation does not un-happen it.
func (d DetachmentViewModel) Current() bool { return d.ReattachedAt == "" }

// Name is what to show. A record that lost its display name still identifies
// its server rather than rendering as nothing.
func (d DetachmentViewModel) Name() string {
	if name := strings.TrimSpace(d.DisplayName); name != "" {
		return name
	}
	return d.ServerID
}

type PublicShareViewModel struct {
	ChannelID, ServerID, RepoID, RepoDisplayName string
	Alias, Slug, State, SourceRoot, UpdatedAt    string
	RecipientCount, ObjectCount                  int
	PasswordProtected                            bool
	FollowHead                                   bool
}

type UpdateViewModel struct {
	State            string
	Channel          string
	CurrentVersion   string
	AvailableVersion string
	ReleaseID        string
	Summary          string
	RestartRequired  bool
}

type RecoveryViewModel struct {
	OperationID, ServerID, ServerName, KitPath, AdminContact string
	ArchiveCount                                             int
	DownloadUntil, AdminGraceUntil                           string
	CanDownload                                              bool
}

func (update *UpdateViewModel) Available() bool {
	return update != nil && update.State == "available" && update.AvailableVersion != ""
}

// ViewModel is the complete read-only presentation model consumed by the tray adapter.
// It is replaced atomically on every state change; the tray layer must not mutate it.
type ViewModel struct {
	Connected           bool
	Stale               bool // true: data predates last disconnect; display but mark stale
	DaemonState         string
	UptimeSec           int64
	LastRefresh         time.Time
	Capabilities        map[string]bool
	Repos               []RepoViewModel
	Servers             []ServerViewModel
	Reservations        []Reservation
	LockReleaseRequests []LockReleaseRequest
	Recoveries          []RecoveryViewModel
	Errors              []ErrorViewModel
	Activity            []ActivityViewModel
	Notices             []NoticeViewModel
	Detachments         []DetachmentViewModel
	PublicShares        []PublicShareViewModel
	PublicSharesKnown   bool
	PendingActions      []PendingAction
	Update              *UpdateViewModel
	Icon                IconState
}

func (vm ViewModel) CanPlanUpdate() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapUpdatePlan) && vm.Update.Available()
}

func (vm ViewModel) CanApplyUpdate() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapUpdateApply) && vm.Update.Available()
}

// HasCap reports whether the daemon advertised the given capability.
func (vm ViewModel) HasCap(cap string) bool { return vm.Capabilities[cap] }

// CanLock, CanUnlock and CanListErrors expose advertised permissions without
// leaking capability names into tray adapters. CanMutateLock and
// CanMutateUnlock additionally apply the live-state gate shared by presenters
// and action controllers.
func (vm ViewModel) CanLock() bool         { return vm.HasCap(contract.CapRepoLock) }
func (vm ViewModel) CanUnlock() bool       { return vm.HasCap(contract.CapRepoUnlock) }
func (vm ViewModel) CanListErrors() bool   { return vm.HasCap(contract.CapErrorList) }
func (vm ViewModel) CanListActivity() bool { return vm.HasCap(contract.CapRepoActivity) }
func (vm ViewModel) CanPublish() bool      { return vm.HasCap(contract.CapRepoPublish) }
func (vm ViewModel) CanAckNotices() bool   { return vm.HasCap(contract.CapNoticeAck) }
func (vm ViewModel) SupportsReservationListing() bool {
	return vm.HasCap(contract.CapRepoReservationList)
}
func (vm ViewModel) CanListReservations() bool {
	return vm.Connected && !vm.Stale && vm.SupportsReservationListing()
}

func (vm ViewModel) CanRequestLockRelease() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapLockReleaseRequest)
}

func (vm ViewModel) CanDismissLockRelease() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapLockReleaseDismiss)
}

func (vm ViewModel) CanAcceptLockRelease() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapLockReleaseAccept)
}
func (vm ViewModel) CanBrowseReservations() bool {
	if !vm.CanListReservations() {
		return false
	}
	for _, server := range vm.Servers {
		if !server.ReservationsKnown {
			continue
		}
		if server.ReservationCount > 0 {
			return true
		}
	}
	return false
}
func (vm ViewModel) CanReleaseReservations() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoReservationList) && vm.HasCap(contract.CapRepoReservationRelease)
}
func (vm ViewModel) CanDetachRepository() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoDetach)
}
func (vm ViewModel) CanDeleteRepository() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoDelete)
}
func (vm ViewModel) CanDismissRecovery() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoRecoveryDismiss)
}
func (vm ViewModel) CanAttachRepository() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoAttachIntent) && vm.HasCap(contract.CapRepoAttachApprove)
}
func (vm ViewModel) CanRepairRepositoryLifecycle() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoLifecycleRepair)
}
func (vm ViewModel) CanLocateRepository() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoLocate)
}
func (vm ViewModel) CanClaimRealmAlias() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRealmAliasClaim)
}
func (vm ViewModel) CanManageRealmGrants() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRealmGrantRecipients) && vm.HasCap(contract.CapRepoGrantAccess) && vm.HasCap(contract.CapRepoRevokeAccess)
}
func (vm ViewModel) CanSetEditingPolicy() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoSetEditingPolicy)
}
func (vm ViewModel) CanManagePublicShares() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoPublicShareList) && vm.HasCap(contract.CapRepoPublicShareCreate) && vm.HasCap(contract.CapRepoPublicShareUpdate) && vm.HasCap(contract.CapRepoPublicShareRevoke) && vm.HasCap(contract.CapRepoPublicShareDelete)
}
func (vm ViewModel) CanManageUploadChannels() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoUploadChannelList) && vm.HasCap(contract.CapRepoUploadChannelCreate) && vm.HasCap(contract.CapRepoUploadChannelUpdate) && vm.HasCap(contract.CapRepoUploadChannelRevoke) && vm.HasCap(contract.CapRepoUploadChannelDelete)
}
func (vm ViewModel) CanReviewQuarantine() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoQuarantineList) && vm.HasCap(contract.CapRepoQuarantineHide) && vm.HasCap(contract.CapRepoQuarantineFetch)
}
func (vm ViewModel) CanSetRealmVisibility() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRealmSetVisibility)
}
func (vm ViewModel) CanSetRealmBranding() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRealmPublicBrandingGet) && vm.HasCap(contract.CapRealmPublicBrandingSet)
}
func (vm ViewModel) CanSetSessionTimeout() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapServerSetSessionTimeout)
}
func (vm ViewModel) CanPairMobile() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapMobilePairingBegin)
}
func (vm ViewModel) CanDetachServer() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapServerDetach)
}
func (vm ViewModel) CanRestartFileES() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapSystemRestart)
}
func (vm ViewModel) CanShutdownFileES() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapSystemShutdown)
}
func (vm ViewModel) CanMutateLock() bool { return vm.Connected && !vm.Stale && vm.CanLock() }
func (vm ViewModel) CanMutateUnlock() bool {
	return vm.Connected && !vm.Stale && vm.CanUnlock()
}

func (r RepoViewModel) CanWrite() bool { return r.Access == contract.AccessReadWrite }

// LocallyProvisioning distinguishes a repository whose server-side creation
// is still running from a genuinely remote repository. During the initial
// import Attached is deliberately false (there is no usable SVN working copy
// yet), but LocalPath remains durable local ownership of the operation.
func (r RepoViewModel) LocallyProvisioning() bool {
	if r.Attached || r.ServerDeleted || strings.TrimSpace(r.LocalPath) == "" {
		return false
	}
	switch r.DisplayState() {
	case RepoDisplayBusy, RepoDisplayInitializing, RepoDisplayBaselining, RepoDisplayAttention, RepoDisplayOffline:
		return true
	default:
		return false
	}
}

// NeedsLocate is the missing-WC signal: the attachment is still claimed, the
// root is gone, and FileES is waiting for the user to point at the moved copy.
func (r RepoViewModel) NeedsLocate() bool {
	return r.Attached && r.CurrentOp != nil && *r.CurrentOp == "working_copy_missing"
}

// DisplayState maps protocol state to the stable vocabulary consumed by UI
// adapters. Unknown future protocol states degrade to RepoDisplayUnknown.
func (r RepoViewModel) DisplayState() RepoDisplayState {
	if r.Conflicts > 0 || r.State == contract.StateDegraded || r.State == contract.StateInteractionRequired {
		return RepoDisplayAttention
	}
	if r.Connectivity == contract.ConnOffline || r.State == contract.StateOffline {
		return RepoDisplayOffline
	}
	if r.CurrentOp != nil {
		return RepoDisplayBusy
	}
	switch r.State {
	case contract.StateActive:
		return RepoDisplayActive
	case contract.StateInitializing:
		return RepoDisplayInitializing
	case contract.StateBaselining:
		return RepoDisplayBaselining
	case contract.StatePaused:
		return RepoDisplayPaused
	case contract.StateStopping:
		return RepoDisplayStopping
	case contract.StateUnattached:
		return RepoDisplayUnattached
	case contract.StateDisabled:
		return RepoDisplayDisabled
	case contract.StateRevoked:
		return RepoDisplayRevoked
	case "deleted":
		return RepoDisplayDeleted
	default:
		return RepoDisplayUnknown
	}
}

// ShowsBusy reports the same busy state that drives the tray icon. Other GUI
// layers use this presentation method instead of importing the wire contract
// and duplicating its state vocabulary.
func (r RepoViewModel) ShowsBusy() bool { return repoIconState(r) == IconBusy }

// aggregateIcon derives the tray icon from the connection status and all repo states.
func aggregateIcon(connected bool, repos []RepoViewModel, notices int) IconState {
	if !connected {
		return IconDisconnected
	}
	if notices > 0 {
		return IconShout
	}
	best := IconActive
	for _, r := range repos {
		icon := repoIconState(r)
		if iconPriority(icon) > iconPriority(best) {
			best = icon
		}
	}
	return best
}

func repoIconState(r RepoViewModel) IconState {
	// A projected optional repository without a local attachment cannot be
	// doing work on this machine. In particular, a remote INITIALIZING record
	// may outlive a failed or abandoned creation attempt. It remains visible
	// in server authority, but must not keep the whole client permanently in
	// the busy state.
	if !r.Attached && r.AttachmentPolicy == "optional" && !r.LocallyProvisioning() {
		return IconActive
	}
	if r.Conflicts > 0 || r.State == contract.StateDegraded || r.State == contract.StateInteractionRequired {
		return IconError
	}
	if r.Connectivity == contract.ConnOffline || r.State == contract.StateOffline {
		return IconOffline
	}
	switch r.State {
	case contract.StateActive:
		if r.CurrentOp != nil {
			return IconBusy
		}
		return IconActive
	case contract.StateInitializing, contract.StateBaselining, contract.StatePaused, contract.StateStopping:
		return IconBusy
	case contract.StateUnattached, contract.StateDisabled:
		return IconActive
	case contract.StateRevoked:
		return IconError
	default:
		return IconBusy // safe fallback for unknown/future states
	}
}

func iconPriority(s IconState) int {
	switch s {
	case IconError:
		return 3
	case IconOffline:
		return 2
	case IconBusy:
		return 1
	default:
		return 0
	}
}
