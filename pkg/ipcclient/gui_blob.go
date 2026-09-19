package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
	"filees/pkg/guiblob"
)

func (c *Client) GUIBlob(ctx context.Context, server string, write *guiblob.Write) (guiblob.State, error) {
	command := contract.CmdGUIBlobGet
	var payload any = contract.GUIBlobGetPayload{ServerID: server}
	if write != nil {
		command = contract.CmdGUIBlobSet
		payload = contract.GUIBlobSetPayload{ServerID: server, Write: *write}
	}
	response, err := c.do(ctx, command, "", payload)
	if err != nil {
		return guiblob.State{}, err
	}
	var state guiblob.State
	if err = contract.DecodeResult(response.Result, &state); err == nil {
		err = state.Validate()
	}
	return state, err
}
