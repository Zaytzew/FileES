package contract

const (
	CmdRepoCommitRecoveryPlan  = "repo.commit_recovery.plan"
	CmdRepoCommitRecoveryApply = "repo.commit_recovery.apply"
	CommitRecoveryRetryQueue   = "retry_preserved_queue"
	CommitRecoveryServerCopy   = "server_wins_preserve_local"
	CommitRecoveryRebuild      = "server_rebuild_preserve_local"
)

// Retry/server-copy require a no-effect proof. Rebuild is a distinct explicit
// decision to replace the entire local WC from the server while archiving it;
// it makes no assertion about the remote effect and never retries the queue.
type CommitRecoveryPlan struct {
	PlanID        string   `json:"plan_id"`
	RepoID        string   `json:"repo_id"`
	TransactionID string   `json:"transaction_id"`
	Choice        string   `json:"choice"`
	FirstRevision int64    `json:"first_revision"`
	HeadRevision  int64    `json:"head_revision"`
	Paths         []string `json:"paths"`
	ExpiresAt     string   `json:"expires_at"`
	Conflicts     []string `json:"conflicts,omitempty"`
	ConflictCopy  string   `json:"conflict_copy,omitempty"`
	Archive       string   `json:"archive,omitempty"`
}

type CommitRecoveryApplyPayload struct {
	PlanID string `json:"plan_id"`
	Choice string `json:"choice"`
}

type CommitRecoveryApplyResult struct {
	PlanID string `json:"plan_id"`
	State  string `json:"state"` // queued, rebuild_requested (internal), rebuilt
}
