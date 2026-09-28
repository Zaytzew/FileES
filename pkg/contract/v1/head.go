package contract

// Head browser for an unattached or sparse repository
// (implementation notes (not distributed)). Reads are HEAD, not a
// working copy. Materialize creates or extends one sparse copy: the first
// path goes through the ordinary attach lifecycle in sparse mode, later ones
// through the running working copy. Fill deepens that same copy to the whole
// tree and does not ask for another path.
// CapRepoHeadBrowse is advertised by a daemon that serves the four commands
// below; the interface offers "Browse on the server" only against it.
const CapRepoHeadBrowse = "repo.head_browse"

// CapRepoExplorerAnchor is advertised by a daemon that can make a folder an
// Explorer anchor: Windows with the filees-cfapi helper installed.
const CapRepoExplorerAnchor = "repo.explorer_anchor"

// CapRepoPartialAnchor says how this daemon does partial attachments: as
// Explorer anchors (Windows builds with the Cloud Files API) and never as
// plain sparse copies. Absent, partial attachments are sparse copies driven
// from the repository browser (Linux, and Windows builds tagged nocfapi).
// One mode per build (owner, 2026-09-28); advertised even when the helper is
// missing, which then makes partial attachment unavailable rather than
// switching to the other mode.
const CapRepoPartialAnchor = "repo.partial_anchor"

// RepoAnchorCreatePayload names the repository and the folder that becomes
// its anchor. The folder must not exist yet or be empty, and must not lie
// inside a folder another provider (Nextcloud, OneDrive) synchronises.
type RepoAnchorCreatePayload struct {
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	LocalPath string `json:"local_path"`
}

const (
	CmdRepoHeadList        = "repo.head_list"
	CmdRepoHeadCat         = "repo.head_cat"
	CmdRepoHeadMaterialize = "repo.head_materialize"
	CmdRepoHeadFill        = "repo.head_fill"
	// CmdRepoAnchorCreate makes a folder an Explorer anchor of a repository
	// without a working copy (Windows; capability repo.explorer_anchor).
	CmdRepoAnchorCreate = "repo.anchor_create"
)

type RepoHeadListPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Path     string `json:"path,omitempty"`
}

type RepoHeadEntry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	// Local: the entry is present in this repository's sparse working copy.
	Local bool `json:"local,omitempty"`
}

type RepoHeadListResult struct {
	Path    string          `json:"path"`
	Entries []RepoHeadEntry `json:"entries"`
}

type RepoHeadCatPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Path     string `json:"path"`
}

type RepoHeadCatResult struct {
	Path string `json:"path"`
	File string `json:"file"`
}

type RepoHeadMaterializePayload struct {
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	Path      string `json:"path"`
	LocalPath string `json:"local_path,omitempty"`
}

type RepoHeadFillPayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
}

type RepoHeadWriteResult struct {
	LocalPath string `json:"local_path"`
	Path      string `json:"path,omitempty"`
	// State is "attached" when the path is already on disk, "attaching" when
	// the first path started a sparse attachment that the lifecycle is still
	// checking out (poll OperationID with repo.lifecycle_status).
	State       string `json:"state"`
	OperationID string `json:"operation_id,omitempty"`
}
