package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) VirtualFolders(ctx context.Context, enabled *bool) (*contract.VirtualFoldersStatus, error) {
	resp, err := c.do(ctx, contract.CmdVirtualFolders, "", contract.VirtualFoldersPayload{Enabled: enabled})
	if err != nil {
		return nil, err
	}
	var result contract.VirtualFoldersStatus
	return &result, contract.DecodeResult(resp.Result, &result)
}
