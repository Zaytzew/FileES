package contract

// Wehikuł czasu reads, concepts/REPOSITORY_HISTORY_CONCEPT.md §6.
//
// repo.history_resolve turns a moment into a snapshot: one repository, the
// UUID it had when resolved, and one revision. A snapshot is a handle, not a
// permission. The daemon re-checks the owner gate on every call and forgets a
// snapshot whose repository URL has changed since.
//
// Commits and changes are paged newest first; a folder listing is paged by
// name. Cursors are opaque to the GUI.
const (
	CapRepoHistory        = "repo.history"
	CmdRepoHistoryResolve = "repo.history_resolve"
	CmdRepoHistoryCommits = "repo.history_commits"
	CmdRepoHistoryChanges = "repo.history_changes"
	CmdRepoHistoryList    = "repo.history_list"
)

const (
	// HistoryBoundaryAt takes the newest commit not later than the moment.
	HistoryBoundaryAt = "at"
	// HistoryBoundaryBefore takes the newest commit strictly earlier.
	HistoryBoundaryBefore = "before"
)

type RepoHistoryResolvePayload struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	// MomentUTC is RFC 3339, read to the microsecond. Empty means now.
	MomentUTC string `json:"moment_utc,omitempty"`
	// Boundary is HistoryBoundaryAt (the default) or HistoryBoundaryBefore.
	Boundary string `json:"boundary,omitempty"`
}

type RepoHistorySnapshot struct {
	SnapshotID      string `json:"snapshot_id"`
	ServerID        string `json:"server_id"`
	RepoID          string `json:"repo_id"`
	RepositoryUUID  string `json:"repository_uuid"`
	Revision        int64  `json:"revision"`
	RequestedMoment string `json:"requested_moment"`
	// SavedAt is the repository date of Revision; empty for r0, when the
	// moment precedes the first commit.
	SavedAt string `json:"saved_at,omitempty"`
}

// RepoHistoryCommitsPayload asks for the commits of an interval. The interval
// is independent of the snapshot's moment; the snapshot names the repository.
type RepoHistoryCommitsPayload struct {
	SnapshotID string `json:"snapshot_id"`
	From       string `json:"from"` // RFC 3339, inclusive
	To         string `json:"to"`   // RFC 3339, inclusive
	Cursor     string `json:"cursor,omitempty"`
}

type RepoHistoryCommit struct {
	Revision int64  `json:"revision"`
	Date     string `json:"date"`
	// Author is svn:author as recorded - today the installation's client ID.
	// Realm and activation nick arrive with §9.2.
	Author       string `json:"author,omitempty"`
	Shout        string `json:"shout,omitempty"`
	ChangedCount int    `json:"changed_count"`
}

type RepoHistoryCommitsResult struct {
	Commits    []RepoHistoryCommit `json:"commits"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type RepoHistoryChangesPayload struct {
	SnapshotID string `json:"snapshot_id"`
	Revision   int64  `json:"revision"`
	Cursor     string `json:"cursor,omitempty"`
}

type RepoHistoryChange struct {
	Path             string `json:"path"`           // repository-relative, no leading slash; "" is the root
	Action           string `json:"action"`         // A, D, M or R
	Kind             string `json:"kind,omitempty"` // file or dir; empty when the server did not say
	CopyFromPath     string `json:"copyfrom_path,omitempty"`
	CopyFromRevision int64  `json:"copyfrom_rev,omitempty"`
}

type RepoHistoryChangesResult struct {
	Revision   int64               `json:"revision"`
	Changed    []RepoHistoryChange `json:"changed"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type RepoHistoryListPayload struct {
	SnapshotID string `json:"snapshot_id"`
	// Path is a repository-relative folder; empty is the root.
	Path   string `json:"path,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type RepoHistoryEntry struct {
	Name                string `json:"name"`
	Kind                string `json:"kind"` // file or dir
	Size                *int64 `json:"size,omitempty"`
	LastChangedRevision int64  `json:"last_changed_revision"`
	LastChangedDate     string `json:"last_changed_date,omitempty"`
	LastAuthor          string `json:"last_author,omitempty"`
}

type RepoHistoryListResult struct {
	Revision   int64              `json:"revision"`
	Path       string             `json:"path"`
	Entries    []RepoHistoryEntry `json:"entries"`
	NextCursor string             `json:"next_cursor,omitempty"`
}
