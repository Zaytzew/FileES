package app

import contract "filees/pkg/contract/v1"

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

// RepoViewModel is the read-only presentation model for one repository.
// Constructed from RepoSummary (URL, LocalPath) + RepoStatus (live state).
type RepoViewModel struct {
	ID           string
	URL          string
	LocalPath    string
	State        string
	Connectivity string
	LocalRev     int64
	HeadRev      int64
	Pending      contract.PendingStats
	Conflicts    int
	LastSyncAt   string
	CurrentOp    *string
}

// ViewModel is the complete read-only presentation model consumed by the tray adapter.
// It is replaced atomically on every state change; the tray layer must not mutate it.
type ViewModel struct {
	Connected    bool
	Stale        bool // true: data predates last disconnect; display but mark stale
	Capabilities map[string]bool
	Repos        []RepoViewModel
	Icon         IconState
}

// HasCap reports whether the daemon advertised the given capability.
func (vm ViewModel) HasCap(cap string) bool { return vm.Capabilities[cap] }

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
