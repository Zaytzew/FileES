package contract

const CmdRepoExplorerShortcut = "repo.explorer_shortcut"
const CapRepoExplorerShortcut = CmdRepoExplorerShortcut

// Visibility belongs to one local WC. Nil reads; no global/server mutation.
type RepoExplorerShortcutPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Visible  *bool  `json:"visible,omitempty"`
	WCID     string `json:"wc_id,omitempty"` // mutation binds the exact lifecycle generation read before confirmation
}
type RepoExplorerShortcutStatus struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Visible  bool   `json:"visible"`
	WCID     string `json:"wc_id"`
}
