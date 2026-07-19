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
type RepoStatus struct {
	RepoID           string       `json:"repo_id"`
	ServerID         string       `json:"server_id"`
	DisplayName      string       `json:"display_name"`
	Attached         bool         `json:"attached"`
	Access           string       `json:"access"`
	State            string       `json:"state"`        // one of the State* constants
	Connectivity     string       `json:"connectivity"` // ConnOnline | ConnOffline
	LocalRevision    int64        `json:"local_revision"`
	HeadRevision     int64        `json:"head_revision"`
	Pending          PendingStats `json:"pending"`
	Conflicts        int          `json:"conflicts"`
	LastSyncAt       string       `json:"last_sync_at,omitempty"` // RFC3339; empty if never synced
	CurrentOperation *string      `json:"current_operation"`      // null or short description
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
