package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) AnchorReleaseQueue(ctx context.Context, p contract.RepoAnchorQueuePayload) (*contract.RepoAnchorQueueResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAnchorReleaseQueue, "", p)
	if err != nil {
		return nil, err
	}
	var result contract.RepoAnchorQueueResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) AnchorReleaseQueueStatus(ctx context.Context, p contract.RepoAnchorQueueStatusPayload) (*contract.RepoAnchorQueueResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAnchorReleaseQueueStatus, "", p)
	if err != nil {
		return nil, err
	}
	var result contract.RepoAnchorQueueResult
	return &result, contract.DecodeResult(resp.Result, &result)
}
