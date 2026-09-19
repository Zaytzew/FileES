package contract

import "filees/pkg/guiblob"

const (
	CmdGUIBlobGet = "realm.gui_blob_get"
	CmdGUIBlobSet = "realm.gui_blob_set"
	CapGUIBlob    = "realm.gui_blob.v1"
)

type GUIBlobGetPayload struct {
	ServerID string `json:"server_id"`
}
type GUIBlobSetPayload struct {
	ServerID string `json:"server_id"`
	guiblob.Write
}
