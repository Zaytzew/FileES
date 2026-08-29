package contract

// Event type constants — stable identifiers in Event.Type (§9).
// Clients must handle unknown event types by ignoring them (§13).
const (
	EvRepoStateChanged    = "repo.state_changed"
	EvRepoCycleChanged    = "repo.cycle_changed"
	EvSyncStarted         = "sync.started"
	EvSyncProgress        = "sync.progress"
	EvSyncCompleted       = "sync.completed"
	EvCommitStarted       = "commit.started"
	EvCommitCompleted     = "commit.completed"
	EvOfflineDetected     = "offline.detected"
	EvOnlineRestored      = "online.restored"
	EvConflictDetected    = "conflict.detected"
	EvInteractionRequired = "interaction.required"
	EvNoticeCreated       = "notice.created"
	EvErrorRaised         = "error.raised"
	EvActivationChanged   = "activation.changed"
	EvProjectionChanged   = "projection.changed"
	EvWhaleChanged        = "whale.changed"
	EvActivityChanged     = "activity.changed"
	EvPublicSharesChanged = "public_shares.changed"
)

// RepoStateChangedPayload is the payload for EvRepoStateChanged.
type RepoStateChangedPayload struct {
	OldState string `json:"old_state"`
	NewState string `json:"new_state"`
}

// SyncProgressPayload is the payload for EvSyncProgress.
type SyncProgressPayload struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// SyncCompletedPayload is the payload for EvSyncCompleted.
type SyncCompletedPayload struct {
	Revision int64 `json:"revision"`
}

// CommitCompletedPayload is the payload for EvCommitCompleted.
type CommitCompletedPayload struct {
	Revision int64 `json:"revision"`
	Paths    int   `json:"paths"`
}

// ConflictDetectedPayload is the payload for EvConflictDetected (§10).
type ConflictDetectedPayload struct {
	DecisionID string `json:"decision_id"`
	Path       string `json:"path"`
}

// ErrorRaisedPayload is the payload for EvErrorRaised.
type ErrorRaisedPayload struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Msg      string `json:"msg"`
}

// NoticeCreatedPayload is the payload for EvNoticeCreated.
type NoticeCreatedPayload struct {
	NoticeID string `json:"notice_id"`
	Title    string `json:"title"`
}
