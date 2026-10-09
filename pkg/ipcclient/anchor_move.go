package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) AnchorMovePlan(ctx context.Context, p contract.RepoAnchorMoveSelection) (*contract.RepoAnchorMovePlan, error) {
	resp, err := c.do(ctx, contract.CmdRepoAnchorMovePlan, "", p)
	if err != nil {
		return nil, err
	}
	var out contract.RepoAnchorMovePlan
	if err := contract.DecodeResult(resp.Result, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) AnchorMove(ctx context.Context, p contract.RepoAnchorMovePayload) (*contract.RepoAnchorMoveResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAnchorMove, "", p)
	if err != nil {
		return nil, err
	}
	var out contract.RepoAnchorMoveResult
	if err := contract.DecodeResult(resp.Result, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) AnchorMoveRecover(ctx context.Context, p contract.RepoAnchorMoveSelection) (*contract.RepoAnchorMoveResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAnchorMoveRecover, "", p)
	if err != nil {
		return nil, err
	}
	var out contract.RepoAnchorMoveResult
	if err := contract.DecodeResult(resp.Result, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
