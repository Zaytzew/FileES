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
	LocalCopy            *contract.LocalCopySummary
	AnchorDownloadBlocks []contract.AnchorDownloadBlock
	AnchorDamage         *contract.AnchorDamage
	ID                   string
	DisplayName          string
	ServerID             string
	Attached             bool
	Access               string
	OwnerRealmID         string
	AttachmentPolicy     string
	EditingPolicy        string
	Purpose              string
	ParentRepoID         string
	// Sparse: attached, holding only paths chosen in the unattached browser.
	Sparse                 bool
	ExplorerShortcut       *bool
	URL                    string
	LocalPath              string
	State                  string
	Connectivity           string
	LocalRev               int64
	HeadRev                int64
	WorkingCopyBytes       int64
	WorkingCopySizeKnown   bool
	Pending                contract.PendingStats
	PublishProgress        *contract.PublishProgress
	Conflicts              int
	UnportableNames        []contract.UnportableName
	LastSyncAt             string
	LastCommitAt           string
	CurrentOp              *string
	ReservationCount       int
	Cycle                  contract.CycleStatus
	ServerDeleted          bool
	LocalCopyPreserved     bool
	LocalCopyStatus        string
	LocalCleanupPending    bool
	RetainUntil            string
	RecoveryOperationID    string
	RecoveryAvailable      bool
	RecoveryPending        bool
	CleanupError           string
	LifecycleOperationID   string
	LifecycleError         string
	CanRetryLifecycle      bool
	CanAbandonLifecycle    bool
	CommitRecoveryRequired bool
}

type PendingAction struct {
	ID                               string
	Kind                             string
	RepoID                           string
	ServerID                         string
	Label                            string
	Phase                            string
	StartedAt                        time.Time
	ExpectedSessionTimeoutMin        int
	ExpectedRepoAttached             bool
	ExpectedRepoDetached             bool
	ExpectedRepoDeleted              bool
	ExpectedIntentsResolved          bool
	ExpectedRecoveryDismissed        bool
	ExpectedLocalProjectionDismissed bool
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
	StorageCapacity       *contract.StorageCapacityStatus
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
	// DemoExpiresAt (RFC 3339) marks a realm on a demo server and when the
	// server removes it; empty for every other server.
	DemoExpiresAt     string
	ReservationCount  int
	ReservationsKnown bool
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

// ErrorViewModel is a presentation-safe structured daemon error.
//
// It carries the daemon's raw Details because the full journal has to show
// them: for a fault the catalogue cannot name they are the only record of what
// happened. The rule they used to be excluded by has not gone away, it has
// moved to where it can be enforced — the tray never receives them, which
// internal/gui/tray asserts.
type ErrorViewModel struct {
	ID        string
	RepoID    string
	Timestamp string
	Code      string
	Severity  string
	Hint      string
	// MessageKey is the dictionary key. The sentence belongs to the daemon's
	// catalogue, which this layer deliberately does not hold: rendering
	// happens where the catalogue is, so one language change reaches the
	// window, the tray and the journal at once.
	MessageKey string
	// MessageDetail is an instance the sentence does not carry — today the
	// path of a reservation issue. It is appended after rendering, never
	// placed inside the wording.
	MessageDetail string
	// Message is what to show when no catalogue rendered this entry: the
	// daemon's own text for entries written before keys were carried.
	Message string
	// Details is the daemon's raw diagnostic text. For a fault the catalogue
	// cannot name it is the only record of what actually happened, so it is
	// carried to the surfaces that can show it as diagnostics. It is never
	// part of a sentence: untranslated, unbounded and frequently English.
	Details string
}

type ActivityViewModel struct {
	RepoID, Path, Kind, Stage, UpdatedAt, ErrorID string
	Revision                                      int64
	Size                                          *int64
}

type NoticeViewModel struct {
	ServerID, Source, Status, Severity, ObservedAt string
	Stale                                          bool
	ID, RepoID, Title, CreatedAt                   string
	Revision                                       int64
	Acked                                          bool
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
	// Cause is "self", "revoked", "demo_expired" or "forced". An expired demo
	// cannot be reactivated by this installation; "forced" was removed here
	// without the server's confirmation, which never revoked the key.
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

func (d DetachmentViewModel) DemoExpired() bool { return d.Cause == "demo_expired" }

func (d DetachmentViewModel) NeedsReactivation() bool { return d.Cause == "revoked" }

// ForceDropped reports a removal made here without the server's confirmation.
func (d DetachmentViewModel) ForceDropped() bool { return d.Cause == "forced" }

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
	ObservedAt                                   string
	Stale                                        bool
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
	// Background fetch of the available release: downloading | ready | failed.
	Download                       string
	DownloadedBytes, DownloadTotal int64
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
	VirtualFolders      *contract.VirtualFoldersStatus
	SyncPause           contract.SyncPauseStatus
	MemorySafety        *contract.MemorySafetyStatus
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
	AnchorReleaseQueue  []contract.RepoAnchorQueueResult
	Activity            []ActivityViewModel
	Notices             []NoticeViewModel
	Detachments         []DetachmentViewModel
	PublicShares        []PublicShareViewModel
	PublicSharesKnown   bool
	PublicSharesLoading bool
	PublicSharesStale   bool
	PendingActions      []PendingAction
	Update              *UpdateViewModel
	Icon                IconState
	// DemoActivation mirrors the daemon: "available" while this installation
	// may still activate the demo server, "used" after it has.
	DemoActivation string
}

// CanActivateDemo offers the demo server only while its one activation is
// unspent; the daemon and the server refuse again regardless.
func (vm ViewModel) CanActivateDemo() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapActivationBegin) && vm.DemoActivation == "available"
}

func (vm ViewModel) CanPlanUpdate() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapUpdatePlan) && vm.Update.Available()
}

func (vm ViewModel) CanApplyUpdate() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapUpdateApply) && vm.Update.Available()
}

// HasCap reports whether the daemon advertised the given capability.
func (vm ViewModel) HasCap(cap string) bool    { return vm.Capabilities[cap] }
func (vm ViewModel) CanExplorerShortcut() bool { return vm.HasCap(contract.CapRepoExplorerShortcut) }

// CanFoldInactive protects current work and attention from presentation-only folding.
func (vm ViewModel) CanFoldInactive(repo RepoViewModel) bool {
	if !vm.Connected || vm.Stale || !repo.Attached || repo.ServerDeleted || repo.DisplayState() != RepoDisplayActive || repo.Pending.Added+repo.Pending.Modified+repo.Pending.Deleted+repo.Pending.Renamed+repo.Pending.RenameUncertain != 0 || repo.CurrentOp != nil || repo.Conflicts != 0 || len(repo.UnportableNames) != 0 || repo.ReservationCount != 0 {
		return false
	}
	for _, action := range vm.PendingActions {
		if action.ServerID == repo.ServerID && (action.RepoID == "" || action.RepoID == repo.ID) {
			return false
		}
	}
	for _, server := range vm.Servers {
		if server.ID == repo.ServerID && (server.ViewSyncedAt == "" || server.ViewSyncFailures > 0 || server.ViewSyncError != "" || server.Detached) {
			return false
		}
	}
	for _, notice := range vm.Notices {
		if notice.RepoID == repo.ID && !notice.Acked {
			return false
		}
	}
	return true
}

// CanLock, CanUnlock and CanListErrors expose advertised permissions without
// leaking capability names into tray adapters. CanMutateLock and
// CanMutateUnlock additionally apply the live-state gate shared by presenters
// and action controllers.
func (vm ViewModel) CanLock() bool                  { return vm.HasCap(contract.CapRepoLock) }
func (vm ViewModel) CanUnlock() bool                { return vm.HasCap(contract.CapRepoUnlock) }
func (vm ViewModel) CanListErrors() bool            { return vm.HasCap(contract.CapErrorList) }
func (vm ViewModel) CanListActivity() bool          { return vm.HasCap(contract.CapRepoActivity) }
func (vm ViewModel) CanPublish() bool               { return vm.HasCap(contract.CapRepoPublish) }
func (vm ViewModel) CanResolveIntents() bool        { return vm.HasCap(contract.CapRepoIntentResolution) }
func (vm ViewModel) CanResolveCommitRecovery() bool { return vm.HasCap(contract.CapRepoCommitRecovery) }
func (vm ViewModel) CanAckNotices() bool            { return vm.HasCap(contract.CapNoticeAck) }
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

func (vm ViewModel) CanDetachDeletedCopy(repo RepoViewModel) bool {
	return vm.CanDetachRepository() && vm.HasCap(contract.CapRepoDetachDeletedCopy) &&
		repo.ServerDeleted && repo.LocalCopyPreserved && !repo.LocalCleanupPending
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
func (vm ViewModel) CanRelocateRepository() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoRelocate)
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

// ShelfParent authorises the same owned parent as shelf settings, without
// confusing the shelf's read-only projection with an ordinary working copy.
func (vm ViewModel) ShelfParent(serverID, shelfID string) (RepoViewModel, bool) {
	if !vm.CanManageUploadChannels() || !vm.HasCap(contract.CapRepoShelfList) {
		return RepoViewModel{}, false
	}
	var shelf RepoViewModel
	found := false
	for _, repo := range vm.Repos {
		if repo.ID == shelfID && repo.ServerID == serverID {
			if found {
				return RepoViewModel{}, false
			}
			shelf, found = repo, true
		}
	}
	if !found || shelf.Purpose != "upload_shelf" || shelf.ParentRepoID == "" || shelf.ServerDeleted {
		return RepoViewModel{}, false
	}
	for _, server := range vm.Servers {
		if server.ID != serverID || !server.CanOfferRepositoryCreation() {
			continue
		}
		for _, parent := range vm.Repos {
			if parent.ServerID == serverID && parent.ID == shelf.ParentRepoID && parent.Purpose == "" && !parent.ServerDeleted && server.Owns(parent) {
				return parent, true
			}
		}
	}
	return RepoViewModel{}, false
}

// CanBrowseHistory is the daemon's offer of Wehikuł czasu. Ownership is a
// separate, per-repository question, and the daemon checks it again per call.
func (vm ViewModel) CanBrowseHistory() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoHistory)
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
	_, pairable := vm.PairableServer()
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapMobilePairingBegin) && pairable
}

// PairableServer answers the first server a phone can be paired with. A demo
// server refuses MOBILE_PAIRING by policy (one activation per realm, since
// 2026-09-17), so offering "Pair" there only produced MOBILE-1002.
func (vm ViewModel) PairableServer() (ServerViewModel, bool) {
	for _, server := range vm.Servers {
		if server.DemoExpiresAt == "" {
			return server, true
		}
	}
	return ServerViewModel{}, false
}
func (vm ViewModel) CanDetachServer() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapServerDetach)
}

// CanForceDropServer gates the fallback offered after an ordinary detach
// failed: removing the server here without its confirmation.
func (vm ViewModel) CanForceDropServer() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapServerForceDrop)
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
	if r.LocalCopy != nil && r.LocalCopy.Lifecycle != "repository_created" {
		return false // restoring a known local copy is not a new import
	}
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

// CanOpenLocalFolder is independent of server/runtime readiness. Presence
// means the recorded directory was observed, not that every file is hydrated.
func (r RepoViewModel) CanOpenLocalFolder() bool {
	if strings.TrimSpace(r.LocalPath) == "" {
		return false
	}
	if r.LocalCopy != nil {
		return r.LocalCopy.Path == r.LocalPath && r.LocalCopy.Presence == "present"
	}
	return r.Attached // compatibility with daemons without local observations
}

// NeedsLocate is the missing-WC signal: the attachment is still claimed, the
// root is gone, and FileES is waiting for the user to point at the moved copy.
func (r RepoViewModel) NeedsLocate() bool {
	return r.Attached && r.CurrentOp != nil && *r.CurrentOp == "working_copy_missing"
}

// DisplayState maps protocol state to the stable vocabulary consumed by UI
// adapters. Unknown future protocol states degrade to RepoDisplayUnknown.
func (r RepoViewModel) DisplayState() RepoDisplayState {
	if r.LocalCopyPreserved {
		if r.LocalCleanupPending || r.LocalCopyStatus != "clean" {
			return RepoDisplayAttention
		}
		return RepoDisplayDeleted
	}
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
	if r.Attached && !r.ServerDeleted && r.CommitRecoveryRequired {
		return IconError
	}
	if r.Attached && !r.ServerDeleted && r.Pending.RenameUncertain > 0 {
		return IconError
	}
	if r.LocalCopyPreserved {
		if r.LocalCleanupPending || r.LocalCopyStatus != "clean" {
			return IconError
		}
		return IconActive
	}
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
