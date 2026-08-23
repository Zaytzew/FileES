package contract

// Repo state constants — closed set; GUI must render unknown states via a safe
// fallback view rather than crashing (§8, §17 criterion 9).
const (
	StateInitializing        = "initializing"
	StateBaselining          = "baselining"
	StateActive              = "active"
	StatePaused              = "paused"
	StateOffline             = "offline"
	StateInteractionRequired = "interaction_required"
	StateDegraded            = "degraded"
	StateStopping            = "stopping"
	StateUnattached          = "unattached"
	StateDisabled            = "disabled"
	StateRevoked             = "revoked"
	StatePolicyPending       = "policy_pending"
	StateAttaching           = "attaching"
	StateAttachmentError     = "attachment_error"
)

// Connectivity values for RepoStatus.Connectivity.
const (
	ConnOnline  = "online"
	ConnOffline = "offline"
)

const (
	AccessReadWrite    = "rw"
	AccessReadOnly     = "r"
	ClientRoleNormal   = "normal"
	ClientRoleReadOnly = "ro"
)

// Decision state values.
const (
	DecisionPending  = "pending"
	DecisionResolved = "resolved"
	DecisionExpired  = "expired"
)

// RepoStatus is the full snapshot returned by CmdRepoStatus (§8).
// It contains everything a GUI needs to render the repo view without reading
// any .filees private files directly.
// Editing policies as they cross the IPC boundary. The default is the empty
// string on this contract too, for the same reason it is on the projection:
// absence is what keeps it invisible to anything that does not know the field.
const (
	EditingFree         = ""
	EditingLockRequired = "lock_required"
)

type RepoStatus struct {
	RepoID           string `json:"repo_id"`
	ServerID         string `json:"server_id"`
	DisplayName      string `json:"display_name"`
	Attached         bool   `json:"attached"`
	Access           string `json:"access"`
	OwnerRealmID     string `json:"owner_realm_id,omitempty"`
	AttachmentPolicy string `json:"attachment_policy"`
	// EditingPolicy is empty for the default and "lock_required" when this
	// repository works through edit passports. Every client needs it, not just
	// the owner: without it a read-only file is unexplained, which is exactly
	// the silent state the concept requires the UI to replace with a reason.
	EditingPolicy    string        `json:"editing_policy,omitempty"`
	State            string        `json:"state"`        // one of the State* constants
	Connectivity     string        `json:"connectivity"` // ConnOnline | ConnOffline
	LocalRevision    int64         `json:"local_revision"`
	HeadRevision     int64         `json:"head_revision"`
	Pending          PendingStats  `json:"pending"`
	Conflicts        int           `json:"conflicts"`
	LastSyncAt       string        `json:"last_sync_at,omitempty"` // RFC3339; empty if never synced
	CurrentOperation *string       `json:"current_operation"`      // null or short description
	Cycle            CycleStatus   `json:"cycle"`
	Recovery         RecoveryStats `json:"recovery"`
}

const (
	CycleWaiting = "waiting"
	CycleRunning = "running"
	CycleStopped = "stopped"
)

// CycleStatus is the daemon-owned schedule of one repository runtime. The GUI
// may turn NextTickAt into a countdown, but must not treat elapsed wall time as
// proof that an action's effect is already present in a snapshot.
type CycleStatus struct {
	ID         uint64 `json:"cycle_id"`
	Phase      string `json:"phase,omitempty"`
	LastTickAt string `json:"last_tick_at,omitempty"`
	NextTickAt string `json:"next_tick_at,omitempty"`
}

// RecoveryStats is a point-in-time diagnostic snapshot of the commit
// service's crash-recovery counters — how much of its work since daemon
// start was resuming or de-duplicating rather than new commits. It has no
// bearing on repo state or user-facing decisions; support/debugging only.
type RecoveryStats struct {
	CacheResumed    int64 `json:"cache_resumed"`
	AlreadyAccepted int64 `json:"already_accepted"`
	CommitBatches   int64 `json:"commit_batches"`
}

// PendingStats summarises changes waiting to be committed.
type PendingStats struct {
	Added      int   `json:"added"`
	Modified   int   `json:"modified"`
	Deleted    int   `json:"deleted"`
	TotalBytes int64 `json:"total_bytes"`
}

// Decision represents a pending user interaction created by the daemon (§10).
// The daemon records it persistently so GUI reconnect sees it via CmdConflictList.
type Decision struct {
	DecisionID string   `json:"decision_id"`
	Type       string   `json:"type"` // e.g. "conflict_resolution"
	RepoID     string   `json:"repo_id"`
	Path       string   `json:"path,omitempty"`
	Options    []string `json:"options"` // exact choices the GUI presents
	Default    string   `json:"default"` // safe choice if user dismisses
	State      string   `json:"state"`   // DecisionPending | DecisionResolved | DecisionExpired
}
