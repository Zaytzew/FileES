package app

import (
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
	RepoDisplayUnknown      RepoDisplayState = "unknown"
)

// RepoViewModel is the read-only presentation model for one repository.
// Constructed from RepoSummary (URL, LocalPath) + RepoStatus (live state).
type RepoViewModel struct {
	ID               string
	DisplayName      string
	ServerID         string
	Attached         bool
	Access           string
	OwnerRealmID     string
	AttachmentPolicy string
	URL              string
	LocalPath        string
	State            string
	Connectivity     string
	LocalRev         int64
	HeadRev          int64
	Pending          contract.PendingStats
	Conflicts        int
	LastSyncAt       string
	CurrentOp        *string
}

type ServerViewModel struct {
	ID                    string
	DisplayName           string
	ClientRole            string
	RealmID               string
	Address               string
	ClientID              string
	SSHPort               int
	CanCreateRepositories bool
	RepositoriesReady     bool
	PendingRequiredRepos  int
	Repos                 []RepoViewModel
}

func (server ServerViewModel) CanOfferRepositoryCreation() bool {
	return server.ClientRole != contract.ClientRoleReadOnly && server.CanCreateRepositories
}

func (server ServerViewModel) Owns(repo RepoViewModel) bool {
	return server.RealmID != "" && repo.OwnerRealmID == server.RealmID
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
	RepoID, Path, Kind, Stage, UpdatedAt string
	Revision                             int64
}

type UpdateViewModel struct {
	State            string
	CurrentVersion   string
	AvailableVersion string
	ReleaseID        string
	Summary          string
	RestartRequired  bool
}

func (update *UpdateViewModel) Available() bool {
	return update != nil && update.State == "available" && update.AvailableVersion != ""
}

// ViewModel is the complete read-only presentation model consumed by the tray adapter.
// It is replaced atomically on every state change; the tray layer must not mutate it.
type ViewModel struct {
	Connected    bool
	Stale        bool // true: data predates last disconnect; display but mark stale
	DaemonState  string
	UptimeSec    int64
	LastRefresh  time.Time
	Capabilities map[string]bool
	Repos        []RepoViewModel
	Servers      []ServerViewModel
	Errors       []ErrorViewModel
	Activity     []ActivityViewModel
	Update       *UpdateViewModel
	Icon         IconState
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
func (vm ViewModel) CanListReservations() bool {
	return vm.Connected && !vm.Stale && vm.HasCap(contract.CapRepoReservationList)
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
	default:
		return RepoDisplayUnknown
	}
}

// aggregateIcon derives the tray icon from the connection status and all repo states.
func aggregateIcon(connected bool, repos []RepoViewModel) IconState {
	if !connected {
		return IconDisconnected
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
