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

// AnchorDamage reports entries of a virtual folder whose cloud metadata
// Windows cannot read. They can be removed only with administrator rights, so
// the daemon reports them and never acts on them itself. Absent when there are
// none. Sample holds at most a few paths relative to the folder.
type AnchorDamage struct {
	Entries int      `json:"entries"`
	Sample  []string `json:"sample,omitempty"`
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
