package contract

// Intent resolution never schedules an SVN move. The only supported choice
// explicitly interprets uncertain additions and missing files as independent
// add/delete operations. Paths and hashes originate in the daemon, not the UI.
const (
	CmdRepoIntentPlan       = "repo.intent.plan"
	CmdRepoIntentApply      = "repo.intent.apply"
	CapRepoIntentResolution = "repo.intent.resolve.v1"
	IntentDeleteAdd         = "delete_add"
)

type IntentPath struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type IntentPlan struct {
	PlanID    string       `json:"plan_id"`
	RepoID    string       `json:"repo_id"`
	Choice    string       `json:"choice"`
	ExpiresAt string       `json:"expires_at"`
	Paths     []IntentPath `json:"paths"`
}

type IntentApplyPayload struct {
	PlanID string `json:"plan_id"`
	Choice string `json:"choice"`
}

type IntentApplyResult struct {
	PlanID string `json:"plan_id"`
	State  string `json:"state"` // queued: decision persisted; not a commit receipt
}
