package contract

// Optional Windows virtual-folder operation. Paths are repository-relative;
// the daemon alone derives activation, attachment, UUID, BASE and operation ID.
const CapRepoAnchorMove = "repo.anchor_move"
const CmdRepoAnchorMovePlan = "repo.anchor_move_plan"
const CmdRepoAnchorMove = "repo.anchor_move"
const CmdRepoAnchorMoveRecover = "repo.anchor_move_recover"

type RepoAnchorMoveSelection struct {
	ServerID    string `json:"server_id"`
	RepoID      string `json:"repo_id"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}
type RepoAnchorMovePlan struct {
	Fingerprint string `json:"fingerprint"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}
type RepoAnchorMovePayload struct {
	RepoAnchorMoveSelection
	Fingerprint string `json:"fingerprint"`
}
type RepoAnchorMoveResult struct {
	State string `json:"state"` // moved; never an unconfirmed remote outcome
}
