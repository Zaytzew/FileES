package contract

const (
	CmdRepoCommitRecoveryPlan  = "repo.commit_recovery.plan"
	CmdRepoCommitRecoveryApply = "repo.commit_recovery.apply"
	CommitRecoveryRetryQueue   = "retry_preserved_queue"
)

// CommitRecoveryPlan describes one failed publication attempt whose remote
// effect has been proven absent at HeadRevision. Applying it retires only the
// attempt marker; the daemon-owned pending queue remains intact.
type CommitRecoveryPlan struct {
	PlanID        string   `json:"plan_id"`
	RepoID        string   `json:"repo_id"`
	TransactionID string   `json:"transaction_id"`
	Choice        string   `json:"choice"`
	FirstRevision int64    `json:"first_revision"`
	HeadRevision  int64    `json:"head_revision"`
	Paths         []string `json:"paths"`
	ExpiresAt     string   `json:"expires_at"`
}

type CommitRecoveryApplyPayload struct {
	PlanID string `json:"plan_id"`
	Choice string `json:"choice"`
}

type CommitRecoveryApplyResult struct {
	PlanID string `json:"plan_id"`
	State  string `json:"state"` // queued: old attempt retired; pending queue preserved
}
