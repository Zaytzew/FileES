package contract

// Separate capability prevents older GUIs from making a global decision.
const CmdVirtualFolders = "server.virtual_folders"
const CapVirtualFolders = CmdVirtualFolders

// Nil reads a server's local preference; an empty server without a mutation
// reads the summary. Availability requires a compatible Windows CFAPI daemon.
type VirtualFoldersPayload struct {
	ServerID string `json:"server_id,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}
type VirtualFoldersStatus struct {
	ServerID string `json:"server_id,omitempty"`
	Enabled  bool   `json:"enabled"`
	// Servers is a system-status summary, never a global enablement switch.
	Servers map[string]bool `json:"servers,omitempty"`
}
