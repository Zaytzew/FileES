package contract

// Optional, daemon-local consent. A decision identifies one refused program
// in one live provider session, not a durable permission or an SVN operation.
const CapRepoAnchorConsent = "repo.anchor_consent"
const CmdRepoAnchorConsent = "repo.anchor_consent"

type AnchorDownloadBlock struct {
	ID      string `json:"id"`
	Program string `json:"program"`
	Files   int    `json:"files"` // distinct requests observed, not a transfer estimate
}

type RepoAnchorConsentPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	BlockID  string `json:"block_id"`
	Allow    bool   `json:"allow"`
}

type RepoAnchorConsentResult struct {
	Allowed bool `json:"allowed"`
}
