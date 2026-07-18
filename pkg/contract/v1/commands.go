package contract

// Command name constants — stable identifiers sent in Request.Command.
// Names follow the <namespace>.<verb> pattern (§7).
const (
	// System
	CmdSystemHello    = "system.hello"    // capability negotiation
	CmdSystemStatus   = "system.status"   // daemon uptime and aggregate state
	CmdSystemShutdown = "system.shutdown" // graceful stop (privileged)

	// Client activation (executed by daemon; GUI only supplies user intent).
	CmdActivationBegin  = "activation.begin"
	CmdActivationFinish = "activation.finish"

	// Repos
	CmdRepoList    = "repo.list"     // list all configured repos
	CmdRepoStatus  = "repo.status"   // snapshot of one repo
	CmdRepoPause   = "repo.pause"    // suspend automatic operations
	CmdRepoResume  = "repo.resume"   // resume after pause
	CmdRepoSyncNow = "repo.sync_now" // request immediate poll/update
	CmdRepoPublish = "repo.publish"  // request immediate commit of pending changes
	CmdRepoLock    = "repo.lock"     // acquire SVN lock on one or more paths
	CmdRepoUnlock  = "repo.unlock"   // release SVN lock on one or more paths

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
	CapEventsSubscribe  = "events.subscribe"
	CapRepoLock         = "repo.lock"
	CapRepoUnlock       = "repo.unlock"
	CapErrorList        = "error.list"
	CapActivationBegin  = "activation.begin"
	CapActivationFinish = "activation.finish"

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
	CapErrorList,
	CapActivationBegin,
	CapActivationFinish,
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
}

type ActivationStatus struct {
	ServerID    string `json:"server_id"`
	DisplayName string `json:"display_name"`
	ClientRole  string `json:"client_role"`
}

type ActivationBeginPayload struct {
	ServerID       string `json:"server_id"`
	ServerAddress  string `json:"server_address"`
	KnownHostsPath string `json:"known_hosts_path"`
	StateRoot      string `json:"state_root"`
	Email          string `json:"email"`
}

type ActivationFinishPayload struct {
	ServerID       string `json:"server_id"`
	ServerAddress  string `json:"server_address"`
	KnownHostsPath string `json:"known_hosts_path"`
	StateRoot      string `json:"state_root"`
	RemotePort     int    `json:"remote_port"`
	OTP            string `json:"otp"`
}

type ActivationCommandResult struct {
	ServerID string `json:"server_id"`
	State    string `json:"state"`
}

// RepoListResult is the result for CmdRepoList.
type RepoListResult struct {
	Repos []RepoSummary `json:"repos"`
}

// RepoSummary is a minimal descriptor used in RepoListResult.
type RepoSummary struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	Access    string `json:"access"`
	URL       string `json:"url"`
	LocalPath string `json:"local_path"`
	State     string `json:"state"`
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
