package contract

// Optional durable release-only queue. An ID is minted once per confirmation;
// replaying that ID reads the same intent, never authorises another batch.
const CapRepoAnchorReleaseQueue = "repo.anchor_release_queue"
const CmdRepoAnchorReleaseQueue = "repo.anchor_release_queue"
const CmdRepoAnchorReleaseQueueStatus = "repo.anchor_release_queue_status"

type RepoAnchorQueuePayload struct {
	RepoAnchorBatchPayload
	IntentID string `json:"intent_id"`
	Files    int64  `json:"files"`
}

type RepoAnchorQueueStatusPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	IntentID string `json:"intent_id"`
}

type RepoAnchorQueueResult struct {
	RepoAnchorBatchResult
	IntentID   string `json:"intent_id"`
	ServerID   string `json:"server_id"`
	RepoID     string `json:"repo_id"`
	UpdatedAt  string `json:"updated_at"`
	Superseded bool   `json:"superseded,omitempty"`
}
