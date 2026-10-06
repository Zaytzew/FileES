package contract

const CmdVirtualFolders = "system.virtual_folders"
const CapVirtualFolders = CmdVirtualFolders

// Nil requests the current local preference, not a change. Availability is
// announced only by a compatible Windows daemon with a CFAPI helper.
type VirtualFoldersPayload struct {
	Enabled *bool `json:"enabled,omitempty"`
}
type VirtualFoldersStatus struct {
	Enabled bool `json:"enabled"`
}
