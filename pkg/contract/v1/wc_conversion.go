package contract

const CapWCConversion = "repo.wc_conversion"
const CmdWCConversion = "repo.wc_conversion"

// Begin binds the confirmed source path. Controls bind the exact conversion
// ID; a stale GUI must not retry or roll back another operation. No FS plan,
// arbitrary destination, file identities or server credentials cross IPC.
type WCConversionPayload struct {
	ServerID    string `json:"server_id"`
	RepoID      string `json:"repo_id"`
	Action      string `json:"action"` // begin | status | retry | rollback
	SourcePath  string `json:"source_path,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
}

type WCConversionStatus struct {
	OperationID   string `json:"operation_id,omitempty"`
	ServerID      string `json:"server_id"`
	RepoID        string `json:"repo_id"`
	SourcePath    string `json:"source_path"`
	RecoveryPath  string `json:"recovery_path,omitempty"`
	Phase         string `json:"phase,omitempty"`
	Error         string `json:"error,omitempty"`
	Running       bool   `json:"running"`
	CanBegin      bool   `json:"can_begin"`
	CanRetry      bool   `json:"can_retry"`
	CanRollback   bool   `json:"can_rollback"`
	CountersKnown bool   `json:"counters_known"`
	Done          int64  `json:"done"`
	Total         int64  `json:"total"`
}
